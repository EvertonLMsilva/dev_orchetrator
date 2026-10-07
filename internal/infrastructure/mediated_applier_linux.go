//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type MediatedWriteRequest struct {
	Workspace     ManagedWorkspace
	Policy        domain.CandidatePolicy
	Artifact      domain.CandidateArtifact
	Context       domain.WriteBinding
	ApprovalID    domain.ApprovalID
	TransactionID string
}

func managedWorkspaceIdentity(w ManagedWorkspace) string {
	return domain.CandidateDigest([]byte(w.WorkspaceID + ":" + w.RootIdentity + ":" + string(w.ProjectID) + ":" + fmtGeneration(w.Generation)))
}
func fmtGeneration(g uint64) string { return fmt.Sprintf("%d", g) }

// WorkspaceIdentity is metadata, never an access path or write capability.
func (w ManagedWorkspace) Identity() string { return managedWorkspaceIdentity(w) }

func (s *ManagedWorkspaceStore) checkpoint(stage string, n int) error {
	if s.fail != nil {
		return s.fail(stage, n)
	}
	return nil
}
func (s *ManagedWorkspaceStore) terminal(db *managedDatabase, j writeJournal, state domain.WriteConsumptionState) (domain.WriteTerminalResult, error) {
	result := domain.WriteTerminalResult{State: state, ResultIdentity: domain.CandidateDigest([]byte(j.TransactionID + ":" + string(state)))}
	if state == domain.WriteApplied {
		result.ResultIdentity = j.Candidate.ExpectedPostIdentity
	}
	r, err := db.Approvals[j.ApprovalID].Finish(j.TransactionID, result)
	if err != nil {
		return domain.WriteTerminalResult{}, err
	}
	j.State = string(state)
	db.Journals[j.TransactionID] = j
	db.Approvals[j.ApprovalID] = r
	// Persist terminal before releasing lease, in two durable transitions.
	if err = s.save(db); err != nil {
		return domain.WriteTerminalResult{}, err
	}
	if err = s.checkpoint("terminal", j.Completed); err != nil {
		return domain.WriteTerminalResult{}, err
	}
	if state != domain.WriteRecoveryRequired {
		w := db.Workspaces[j.Workspace.WorkspaceID]
		w.Lease = nil
		db.Workspaces[j.Workspace.WorkspaceID] = w
		if err = s.save(db); err != nil {
			return domain.WriteTerminalResult{}, err
		}
	}
	return result, nil
}
func (s *ManagedWorkspaceStore) Apply(ctx context.Context, q MediatedWriteRequest) (domain.WriteTerminalResult, error) {
	var result domain.WriteTerminalResult
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		c := q.Artifact.Candidate()
		r, ok := db.Approvals[q.ApprovalID]
		if !ok || r.TransactionID != q.TransactionID || r.State == domain.WriteAvailable || q.Policy.Validate() != nil || c.ProjectID != q.Workspace.ProjectID || c.WorkspaceIdentity != q.Workspace.Identity() || c.OperationParametersIdentity != mediatedParametersIdentity(q.Policy) || domain.ValidateWriteAuthorization(r.Approval, c, q.Context, r.ReservedAt) != nil {
			return domain.ErrWriteDenied
		}
		root, err := s.openWorkspace(db, q.Workspace)
		if err != nil {
			return err
		}
		defer root.Close()
		if r.State != domain.WriteReserved {
			j, ok := db.Journals[q.TransactionID]
			if !ok || j.Candidate.ArtifactID != c.ArtifactID {
				return ErrManagedWriteBlocked
			}
			if r.State == domain.WriteRecoveryRequired {
				return ErrManagedWriteBlocked
			}
			result = r.Result
			return nil
		}
		w := db.Workspaces[q.Workspace.WorkspaceID]
		if w.Lease != nil {
			return ErrManagedWriteBlocked
		}
		w.Lease = &managedLease{WorkspaceID: q.Workspace.WorkspaceID, Generation: q.Workspace.Generation, TransactionID: q.TransactionID, OwnerInstanceID: s.instance, AcquiredAt: time.Now().UTC(), State: "HELD", Version: 1}
		db.Workspaces[q.Workspace.WorkspaceID] = w
		j := writeJournal{TransactionID: q.TransactionID, ApprovalID: q.ApprovalID, Workspace: q.Workspace, Candidate: c, Policy: q.Policy.Clone(), State: "PREPARED", Preimages: map[string]domain.CandidateFile{}, Postimages: map[string]domain.CandidateFile{}}
		db.Journals[q.TransactionID] = j
		if err = s.save(db); err != nil {
			return err
		}
		if err = s.checkpoint("lease", 0); err != nil {
			return err
		}
		// Everything granting write authority is revalidated under the durable lease.
		verifyRoot, err := s.openWorkspace(db, q.Workspace)
		if err != nil {
			return err
		}
		verifyRoot.Close()
		before, err := managedSnapshot(ctx, int(root.Fd()), q.Policy)
		if err != nil || candidateStateIdentity(q.Policy, before) != c.BaseIdentity {
			// Invalid preconditions still consume approval; journal records denial.
			db.Journals[q.TransactionID] = j
			var finishErr error
			result, finishErr = s.terminal(db, j, domain.WriteAborted)
			if finishErr != nil {
				return finishErr
			}
			return domain.ErrWriteDenied
		}
		j.Preimages = before
		// Recheck the exact RESERVED approval under the lease after base validation.
		reserved := db.Approvals[q.ApprovalID]
		if reserved.State != domain.WriteReserved || reserved.TransactionID != q.TransactionID || domain.ValidateWriteAuthorization(reserved.Approval, c, q.Context, reserved.ReservedAt) != nil {
			return domain.ErrWriteDenied
		}
		j.Parents, err = managedParentIdentities(int(root.Fd()), q.Policy)
		if err != nil {
			return err
		}
		for path, file := range before {
			j.Postimages[path] = file.Clone()
		}
		allowed := map[string]bool{}
		for _, path := range q.Policy.WriteTargets {
			allowed[path] = true
		}
		var total int64
		for _, e := range c.Manifest.Entries {
			content := q.Artifact.Blob(e.Postimage)
			total += int64(len(content))
			old, exists := before[e.Target]
			if !allowed[e.Target] || q.Policy.Excluded(e.Target) || domain.CandidateDigest(content) != e.Postimage || int64(len(content)) > q.Policy.Limits.MaxFileBytes || total > q.Policy.Limits.MaxTotalBytes || (e.Operation == domain.WriteCreate && exists) || (e.Operation == domain.WriteReplace && (!exists || old.Identity != e.Preimage || old.Mode != e.BeforeMode)) {
				db.Journals[q.TransactionID] = j
				result, err = s.terminal(db, j, domain.WriteAborted)
				if err != nil {
					return err
				}
				return domain.ErrWriteDenied
			}
			j.Postimages[e.Target] = domain.CandidateFile{Target: e.Target, Identity: e.Postimage, Mode: e.AfterMode, Content: content}
			// Parents must exist already. Application never widens authority to mkdir.
			parent, _, err := candidateParent(int(root.Fd()), e.Target, false, true)
			if err != nil {
				db.Journals[q.TransactionID] = j
				result, err = s.terminal(db, j, domain.WriteAborted)
				if err != nil {
					return err
				}
				return domain.ErrWriteDenied
			}
			parent.Close()
		}
		var postTotal int64
		for _, file := range j.Postimages {
			postTotal += int64(len(file.Content))
		}
		if postTotal > q.Policy.Limits.MaxTotalBytes || len(c.Manifest.Entries) > q.Policy.Limits.MaxOperations || candidateStateIdentity(q.Policy, j.Postimages) != c.ExpectedPostIdentity {
			db.Journals[q.TransactionID] = j
			result, err = s.terminal(db, j, domain.WriteAborted)
			if err != nil {
				return err
			}
			return domain.ErrWriteDenied
		}
		j.Validated = true
		db.Journals[q.TransactionID] = j
		if err = s.save(db); err != nil {
			return err
		}
		if err = s.checkpoint("prepared", 0); err != nil {
			return err
		}
		j.State = "APPLYING"
		db.Journals[q.TransactionID] = j
		if err = s.save(db); err != nil {
			return err
		}
		for i, e := range c.Manifest.Entries {
			if err = s.checkpoint("before-target", i); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return s.reconcileFailure(db, j, root, &result, err)
			}
			current, readErr := candidateRead(ctx, int(root.Fd()), e.Target, q.Policy.Limits.MaxFileBytes, true)
			if e.Operation == domain.WriteCreate {
				if !errors.Is(readErr, unix.ENOENT) {
					return s.reconcileFailure(db, j, root, &result, domain.ErrWriteDenied)
				}
			} else if readErr != nil || current.Identity != e.Preimage || current.Mode != e.BeforeMode {
				return s.reconcileFailure(db, j, root, &result, domain.ErrWriteDenied)
			}
			if err = s.promote(ctx, db, root, q.Workspace, e, j.Postimages[e.Target].Content, i); err != nil {
				return s.reconcileFailure(db, j, root, &result, err)
			}
			if err = s.checkpoint("promoted", i); err != nil {
				return err
			}
			post, err := candidateRead(ctx, int(root.Fd()), e.Target, q.Policy.Limits.MaxFileBytes, true)
			if err != nil || post.Identity != e.Postimage || post.Mode != e.AfterMode {
				return s.reconcileFailure(db, j, root, &result, domain.ErrWriteDenied)
			}
			j.Completed = i + 1
			db.Journals[q.TransactionID] = j
			if err = s.save(db); err != nil {
				return err
			}
			if err = s.checkpoint("progress", i); err != nil {
				return err
			}
		}
		actual, err := managedSnapshot(ctx, int(root.Fd()), q.Policy)
		if err != nil || candidateStateIdentity(q.Policy, actual) != c.ExpectedPostIdentity {
			return s.reconcileFailure(db, j, root, &result, domain.ErrWriteDenied)
		}
		result, err = s.terminal(db, j, domain.WriteApplied)
		return err
	})
	return result, err
}
func (s *ManagedWorkspaceStore) promote(ctx context.Context, db *managedDatabase, root *os.File, w ManagedWorkspace, e domain.WriteManifestEntry, content []byte, index int) error {
	parent, name, err := candidateParent(int(root.Fd()), e.Target, false, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	tmp, err := candidateNewName()
	if err != nil {
		return err
	}
	// Materialize outside the workload root, then promote relative to pinned fds.
	fd, err := unix.Openat(int(s.control.Fd()), tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, err = f.Write(content)
	if err == nil {
		err = unix.Fchmod(fd, e.AfterMode)
	}
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	staged, err := candidateRead(ctx, int(s.control.Fd()), tmp, 16<<20, false)
	if err != nil || staged.Identity != e.Postimage || staged.Mode != e.AfterMode {
		return domain.ErrWriteDenied
	}
	if err = s.checkpoint("staged", index); err != nil {
		return err
	}
	fresh, err := s.openWorkspace(db, w)
	if err != nil {
		return err
	}
	defer fresh.Close()
	again, againName, err := candidateParent(int(fresh.Fd()), e.Target, false, true)
	if err != nil {
		return err
	}
	defer again.Close()
	var a, b unix.Stat_t
	if unix.Fstat(int(parent.Fd()), &a) != nil || unix.Fstat(int(again.Fd()), &b) != nil || a.Dev != b.Dev || a.Ino != b.Ino || againName != name {
		return ErrManagedWriteBlocked
	}
	// Check the complete expected intermediate state while retaining the lease.
	for _, j := range db.Journals {
		if j.Workspace != w || j.State != "APPLYING" {
			continue
		}
		if !managedParentsMatch(int(fresh.Fd()), j) {
			return ErrManagedWriteBlocked
		}
		want := map[string]domain.CandidateFile{}
		for path, f := range j.Preimages {
			want[path] = f
		}
		for _, entry := range j.Candidate.Manifest.Entries[:j.Completed] {
			want[entry.Target] = j.Postimages[entry.Target]
		}
		actual, err := managedSnapshot(ctx, int(fresh.Fd()), j.Policy)
		if err != nil || candidateStateIdentity(j.Policy, actual) != candidateStateIdentity(j.Policy, want) {
			return domain.ErrWriteDenied
		}
	}
	old, readErr := candidateRead(ctx, int(fresh.Fd()), e.Target, 16<<20, true)
	if e.Operation == domain.WriteCreate {
		if !errors.Is(readErr, unix.ENOENT) {
			return domain.ErrWriteDenied
		}
	} else if readErr != nil || old.Identity != e.Preimage || old.Mode != e.BeforeMode {
		return domain.ErrWriteDenied
	}
	flags := uint(0)
	if e.Operation == domain.WriteCreate {
		flags = unix.RENAME_NOREPLACE
	}
	if err = unix.Renameat2(int(s.control.Fd()), tmp, int(parent.Fd()), name, flags); err != nil {
		return err
	}
	s.effects++
	if err = parent.Sync(); err != nil {
		return err
	}
	return s.control.Sync()
}
func (s *ManagedWorkspaceStore) reconcileFailure(db *managedDatabase, j writeJournal, root *os.File, result *domain.WriteTerminalResult, cause error) error {
	state := s.reconcileState(j, root)
	verified, verifyErr := s.openWorkspace(db, j.Workspace)
	if verifyErr != nil {
		state = domain.WriteRecoveryRequired
	} else {
		verified.Close()
	}
	r, err := s.terminal(db, j, state)
	*result = r
	if err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
func (s *ManagedWorkspaceStore) reconcileState(j writeJournal, root *os.File) domain.WriteConsumptionState {
	if j.Validated && !managedParentsMatch(int(root.Fd()), j) {
		return domain.WriteRecoveryRequired
	}
	files, err := managedSnapshot(context.Background(), int(root.Fd()), j.Policy)
	if err != nil {
		return domain.WriteRecoveryRequired
	}
	actual := candidateStateIdentity(j.Policy, files)
	if actual == j.Candidate.ExpectedPostIdentity && j.State == "APPLYING" {
		// Reconciliation must also make the observed post-state durable before
		// persisting APPLIED, including recovery from a failed directory fsync.
		for _, entry := range j.Candidate.Manifest.Entries {
			parent, name, err := candidateParent(int(root.Fd()), entry.Target, false, true)
			if err != nil {
				return domain.WriteRecoveryRequired
			}
			fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err == nil {
				err = unix.Fsync(fd)
				unix.Close(fd)
			}
			if err == nil {
				err = parent.Sync()
			}
			parent.Close()
			if err != nil {
				return domain.WriteRecoveryRequired
			}
		}
		if root.Sync() != nil || s.control.Sync() != nil {
			return domain.WriteRecoveryRequired
		}
		return domain.WriteApplied
	}
	if actual == j.Candidate.BaseIdentity {
		return domain.WriteAborted
	}
	return domain.WriteRecoveryRequired
}

// Recover never promotes, rolls back or steals a lease. It only reconciles
// known pre/postimages and durably closes a transaction with an exact state.
func (s *ManagedWorkspaceStore) Recover(ctx context.Context, w ManagedWorkspace) (domain.WriteTerminalResult, error) {
	var result domain.WriteTerminalResult
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		root, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		defer root.Close()
		record := db.Workspaces[w.WorkspaceID]
		if record.Lease == nil {
			return domain.ErrWriteDenied
		}
		j, ok := db.Journals[record.Lease.TransactionID]
		if !ok {
			return ErrManagedWriteBlocked
		}
		if j.State == string(domain.WriteRecoveryRequired) {
			return ErrManagedWriteBlocked
		}
		if j.State == string(domain.WriteApplied) || j.State == string(domain.WriteAborted) {
			result = db.Approvals[j.ApprovalID].Result
			record.Lease = nil
			db.Workspaces[w.WorkspaceID] = record
			return s.save(db)
		}
		state := s.reconcileState(j, root)
		result, err = s.terminal(db, j, state)
		if err != nil {
			return err
		}
		if state == domain.WriteRecoveryRequired {
			return ErrManagedWriteBlocked
		}
		return nil
	})
	return result, err
}
