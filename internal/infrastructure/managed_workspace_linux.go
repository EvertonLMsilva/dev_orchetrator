//go:build linux

package infrastructure

import (
	"bytes"
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var ErrManagedWriteBlocked = errors.New("managed write blocked; reconciliation required")

// This capability describes only a provisioned workspace. It contains no path.
// All entry points belong to trusted composition, never to a workload/provider.
type ManagedWorkspace struct {
	WorkspaceID  string
	Generation   uint64
	RootIdentity string
	ProjectID    domain.ProjectID
}
type managedRecord struct {
	Workspace ManagedWorkspace
	Exposed   bool
	Lease     *managedLease
}
type managedLease struct {
	WorkspaceID     string
	Generation      uint64
	TransactionID   string
	OwnerInstanceID string
	AcquiredAt      time.Time
	State           string
	Version         uint64
}
type writeJournal struct {
	TransactionID string
	ApprovalID    domain.ApprovalID
	Workspace     ManagedWorkspace
	Candidate     domain.WriteCandidate
	Policy        domain.CandidatePolicy
	State         string
	Preimages     map[string]domain.CandidateFile
	Postimages    map[string]domain.CandidateFile
	Completed     int
	Validated     bool
	Parents       map[string]string
}
type managedDatabase struct {
	Version    int
	Workspaces map[string]managedRecord
	Approvals  map[domain.ApprovalID]domain.WriteApprovalRecord
	Journals   map[string]writeJournal
}
type managedEnvelope struct {
	Digest string
	Data   json.RawMessage
}

// A private 0700 control directory contains registry, persistent leases and
// journal; roots are freshly provisioned children, never imported directories.
// flock serializes instances, but persistent leases survive lock/process exit.
// Host administrators and processes able to bypass ownership are out of scope.
type ManagedWorkspaceStore struct {
	mu          sync.Mutex
	control     *os.File
	lock        *os.File
	instance    string
	controlPath string
	dev         uint64
	ino         uint64
	effects     int
	// Failure/crash seams remain private to trusted same-package tests.
	fail         func(string, int) error
	persistFault func() error
}

var _ ports.WriteApprovalStore = (*ManagedWorkspaceStore)(nil)

func NewManagedWorkspaceStore(controlPath, instance string) (*ManagedWorkspaceStore, error) {
	if (domain.CandidateContext{ProjectID: "managed", TaskID: "store", CorrelationID: instance}).Validate() != nil {
		return nil, domain.ErrWriteDenied
	}
	root, err := candidateAbsoluteDirectory(controlPath)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(int(root.Fd()), &st) != nil || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
		root.Close()
		return nil, domain.ErrWriteDenied
	}
	fd, err := unix.Openat(int(root.Fd()), "authority.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		root.Close()
		return nil, err
	}
	var ls unix.Stat_t
	if unix.Fstat(fd, &ls) != nil || ls.Mode&unix.S_IFMT != unix.S_IFREG || ls.Nlink != 1 || ls.Mode&07777 != 0600 || ls.Uid != uint32(os.Geteuid()) {
		unix.Close(fd)
		root.Close()
		return nil, domain.ErrWriteDenied
	}
	s := &ManagedWorkspaceStore{control: root, lock: os.NewFile(uintptr(fd), "authority.lock"), instance: instance, controlPath: controlPath, dev: uint64(st.Dev), ino: st.Ino}
	err = s.exclusive(context.Background(), func(db *managedDatabase) error { return nil })
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *ManagedWorkspaceStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.lock.Close(), s.control.Close())
}
func (s *ManagedWorkspaceStore) exclusive(ctx context.Context, fn func(*managedDatabase) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Nonblocking acquisition is fail-closed and gives bounded contention latency.
	if err := unix.Flock(int(s.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return ErrManagedWriteBlocked
	}
	defer unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	physical, err := candidateAbsoluteDirectory(s.controlPath)
	if err != nil {
		return ErrManagedWriteBlocked
	}
	defer physical.Close()
	var st, ls, pinned unix.Stat_t
	if unix.Fstat(int(physical.Fd()), &st) != nil || uint64(st.Dev) != s.dev || st.Ino != s.ino || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) || unix.Fstatat(int(s.control.Fd()), "authority.lock", &ls, unix.AT_SYMLINK_NOFOLLOW) != nil || unix.Fstat(int(s.lock.Fd()), &pinned) != nil || ls.Dev != pinned.Dev || ls.Ino != pinned.Ino || ls.Nlink != 1 {
		return ErrManagedWriteBlocked
	}
	db, err := s.load()
	if err != nil {
		return err
	}
	return fn(&db)
}
func (s *ManagedWorkspaceStore) load() (managedDatabase, error) {
	empty := managedDatabase{Version: 1, Workspaces: map[string]managedRecord{}, Approvals: map[domain.ApprovalID]domain.WriteApprovalRecord{}, Journals: map[string]writeJournal{}}
	fd, err := unix.Openat(int(s.control.Fd()), "state.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		// Absence is initial only if there are no registered/provisioned roots.
		d, e := candidateDirectory(int(s.control.Fd()), ".")
		if e != nil {
			return empty, e
		}
		defer d.Close()
		entries, e := d.ReadDir(4097)
		if e != nil && e != io.EOF {
			return empty, ErrManagedWriteBlocked
		}
		for _, entry := range entries {
			if entry.Name() != "authority.lock" {
				return empty, ErrManagedWriteBlocked
			}
		}
		return empty, nil
	}
	if err != nil {
		return empty, ErrManagedWriteBlocked
	}
	f := os.NewFile(uintptr(fd), "state.json")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Size > 192<<20 {
		return empty, ErrManagedWriteBlocked
	}
	data, err := io.ReadAll(io.LimitReader(f, (192<<20)+1))
	if err != nil {
		return empty, ErrManagedWriteBlocked
	}
	var e managedEnvelope
	if json.Unmarshal(data, &e) != nil || e.Digest != domain.CandidateDigest(e.Data) {
		return empty, ErrManagedWriteBlocked
	}
	canonicalEnvelope, _ := json.Marshal(e)
	if !bytes.Equal(canonicalEnvelope, data) {
		return empty, ErrManagedWriteBlocked
	}
	var db managedDatabase
	if json.Unmarshal(e.Data, &db) != nil || db.Version != 1 || db.Workspaces == nil || db.Approvals == nil || db.Journals == nil {
		return empty, ErrManagedWriteBlocked
	}
	// Canonical encoding rejects duplicate/unknown fields and incomplete records.
	canonical, _ := json.Marshal(db)
	if !bytes.Equal(canonical, e.Data) {
		return empty, ErrManagedWriteBlocked
	}
	seenNonces, seenTransactions := map[string]bool{}, map[string]bool{}
	for id, r := range db.Approvals {
		if id != r.Approval.ApprovalID || r.Validate() != nil {
			return empty, ErrManagedWriteBlocked
		}
		if seenNonces[r.Approval.Nonce] || r.TransactionID != "" && seenTransactions[r.TransactionID] {
			return empty, ErrManagedWriteBlocked
		}
		seenNonces[r.Approval.Nonce] = true
		if r.TransactionID != "" {
			seenTransactions[r.TransactionID] = true
		}
	}
	for id, r := range db.Workspaces {
		if id != r.Workspace.WorkspaceID || !validManagedWorkspace(r.Workspace) {
			return empty, ErrManagedWriteBlocked
		}
		if r.Lease != nil {
			l := r.Lease
			if l.WorkspaceID != id || l.Generation != r.Workspace.Generation || l.TransactionID == "" || l.OwnerInstanceID == "" || l.AcquiredAt.IsZero() || l.State != "HELD" || l.Version != 1 {
				return empty, ErrManagedWriteBlocked
			}
			if _, ok := db.Journals[l.TransactionID]; !ok {
				return empty, ErrManagedWriteBlocked
			}
		}
	}
	for id, j := range db.Journals {
		r, ok := db.Approvals[j.ApprovalID]
		w, exists := db.Workspaces[j.Workspace.WorkspaceID]
		if id != j.TransactionID || !ok || !exists || w.Workspace != j.Workspace || r.TransactionID != id || j.Candidate.Validate() != nil || j.Policy.Validate() != nil || j.Completed < 0 || j.Completed > len(j.Candidate.Manifest.Entries) || j.Preimages == nil || j.Postimages == nil {
			return empty, ErrManagedWriteBlocked
		}
		switch j.State {
		case "PREPARED", "APPLYING", string(domain.WriteApplied), string(domain.WriteAborted), string(domain.WriteRecoveryRequired):
		default:
			return empty, ErrManagedWriteBlocked
		}
		if (j.State == "PREPARED" || j.State == "APPLYING" || j.State == string(domain.WriteRecoveryRequired)) && (w.Lease == nil || w.Lease.TransactionID != id) {
			return empty, ErrManagedWriteBlocked
		}
		if j.Candidate.WorkspaceIdentity != j.Workspace.Identity() || j.Candidate.ProjectID != j.Workspace.ProjectID || j.Candidate.OperationParametersIdentity != mediatedParametersIdentity(j.Policy) || domain.ValidateWriteAuthorization(r.Approval, j.Candidate, r.Approval.WriteBinding, r.ReservedAt) != nil {
			return empty, ErrManagedWriteBlocked
		}
		if j.State == "PREPARED" || j.State == "APPLYING" {
			if r.State != domain.WriteReserved {
				return empty, ErrManagedWriteBlocked
			}
		} else if string(r.State) != j.State {
			return empty, ErrManagedWriteBlocked
		}
		if (j.State == "APPLYING" || j.State == string(domain.WriteApplied)) && !j.Validated {
			return empty, ErrManagedWriteBlocked
		}
		if j.Validated {
			if len(j.Parents) != len(j.Policy.Targets()) {
				return empty, ErrManagedWriteBlocked
			}
			if candidateStateIdentity(j.Policy, j.Preimages) != j.Candidate.BaseIdentity || candidateStateIdentity(j.Policy, j.Postimages) != j.Candidate.ExpectedPostIdentity {
				return empty, ErrManagedWriteBlocked
			}
			allowedImages := map[string]bool{}
			for _, target := range j.Policy.Targets() {
				allowedImages[target] = true
			}
			for _, images := range []map[string]domain.CandidateFile{j.Preimages, j.Postimages} {
				for path, f := range images {
					if !allowedImages[path] || (f.Mode != 0600 && f.Mode != 0644 && f.Mode != 0755) || f.Target != path || domain.CandidateDigest(f.Content) != f.Identity || len(f.Content) > int(j.Policy.Limits.MaxFileBytes) {
						return empty, ErrManagedWriteBlocked
					}
				}
			}
			for _, entry := range j.Candidate.Manifest.Entries {
				pre, exists := j.Preimages[entry.Target]
				post, ok := j.Postimages[entry.Target]
				if !ok || post.Identity != entry.Postimage || post.Mode != entry.AfterMode || (entry.Operation == domain.WriteCreate && exists) || (entry.Operation == domain.WriteReplace && (!exists || pre.Identity != entry.Preimage || pre.Mode != entry.BeforeMode)) {
					return empty, ErrManagedWriteBlocked
				}
			}
		}
	}
	return db, nil
}

func validManagedWorkspace(w ManagedWorkspace) bool {
	if w.Generation != 1 || !strings.HasPrefix(w.WorkspaceID, "candidate-") || len(w.WorkspaceID) != 58 || len(w.RootIdentity) != 64 || strings.ToLower(w.WorkspaceID) != w.WorkspaceID || strings.ToLower(w.RootIdentity) != w.RootIdentity {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(w.WorkspaceID, "candidate-")); err != nil {
		return false
	}
	if _, err := hex.DecodeString(w.RootIdentity); err != nil {
		return false
	}
	return (domain.CandidateContext{ProjectID: w.ProjectID, TaskID: "managed", CorrelationID: "workspace"}).Validate() == nil
}

func managedParentIdentities(root int, p domain.CandidatePolicy) (map[string]string, error) {
	out := map[string]string{}
	for _, target := range p.Targets() {
		parent, _, err := candidateParent(root, target, false, true)
		if err != nil {
			return nil, err
		}
		var st unix.Stat_t
		err = unix.Fstat(int(parent.Fd()), &st)
		parent.Close()
		if err != nil {
			return nil, err
		}
		out[target] = managedRootIdentity(st)
	}
	return out, nil
}
func managedParentsMatch(root int, j writeJournal) bool {
	actual, err := managedParentIdentities(root, j.Policy)
	if err != nil || len(actual) != len(j.Parents) {
		return false
	}
	for path, id := range actual {
		if j.Parents[path] != id {
			return false
		}
	}
	return true
}
func (s *ManagedWorkspaceStore) save(db *managedDatabase) error {
	if s.persistFault != nil {
		if err := s.persistFault(); err != nil {
			return err
		}
	}
	data, err := json.Marshal(db)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(managedEnvelope{domain.CandidateDigest(data), data})
	if err != nil || len(encoded) > 192<<20 {
		return ErrManagedWriteBlocked
	}
	name, err := candidateNewName()
	if err != nil {
		return err
	}
	fd, err := unix.Openat(int(s.control.Fd()), name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	_, err = f.Write(encoded)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = unix.Renameat(int(s.control.Fd()), name, int(s.control.Fd()), "state.json"); err != nil {
		return err
	}
	return s.control.Sync()
}
func managedRootIdentity(st unix.Stat_t) string {
	return domain.CandidateDigest([]byte(fmt.Sprintf("%d:%d", st.Dev, st.Ino)))
}
func (s *ManagedWorkspaceStore) openWorkspace(db *managedDatabase, w ManagedWorkspace) (*os.File, error) {
	r, ok := db.Workspaces[w.WorkspaceID]
	if !ok || r.Exposed || r.Workspace != w {
		return nil, domain.ErrWriteDenied
	}
	root, err := candidateDirectory(int(s.control.Fd()), w.WorkspaceID)
	if err != nil {
		return nil, ErrManagedWriteBlocked
	}
	var st unix.Stat_t
	if unix.Fstat(int(root.Fd()), &st) != nil || managedRootIdentity(st) != w.RootIdentity || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
		root.Close()
		return nil, ErrManagedWriteBlocked
	}
	return root, nil
}

// Provision is the only initial writer. Fixtures are trusted explicit bytes,
// with portable paths/modes validated by the existing candidate contract.
func (s *ManagedWorkspaceStore) Provision(ctx context.Context, project domain.ProjectID, files map[string][]byte) (ManagedWorkspace, error) {
	var w ManagedWorkspace
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		if (domain.CandidateContext{ProjectID: project, TaskID: "provision", CorrelationID: s.instance}).Validate() != nil {
			return domain.ErrWriteDenied
		}
		entries := []domain.WriteManifestEntry{}
		var total int
		for path, b := range files {
			total += len(b)
			if len(b) > 16<<20 || total > 64<<20 {
				return domain.ErrWriteDenied
			}
			entries = append(entries, domain.WriteManifestEntry{Target: path, Operation: domain.WriteCreate, FileType: "regular", Postimage: domain.CandidateDigest(b), AfterMode: 0644})
		}
		if len(entries) > 0 {
			if _, err := (domain.WriteManifest{SchemaVersion: 1, Entries: entries}).Identity(); err != nil {
				return err
			}
		}
		name, err := candidateNewName()
		if err != nil {
			return err
		}
		if err = unix.Mkdirat(int(s.control.Fd()), name, 0700); err != nil {
			return err
		}
		root, err := candidateDirectory(int(s.control.Fd()), name)
		if err != nil {
			return err
		}
		defer root.Close()
		var st unix.Stat_t
		if unix.Fstat(int(root.Fd()), &st) != nil {
			return ErrManagedWriteBlocked
		}
		w = ManagedWorkspace{name, 1, managedRootIdentity(st), project}
		for _, e := range entries {
			parent, n, err := candidateParent(int(root.Fd()), e.Target, true, false)
			if err != nil {
				return err
			}
			fd, err := unix.Openat(int(parent.Fd()), n, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0644)
			if err != nil {
				parent.Close()
				return err
			}
			f := os.NewFile(uintptr(fd), n)
			_, err = f.Write(files[e.Target])
			if err == nil {
				err = f.Sync()
			}
			f.Close()
			if err == nil {
				err = parent.Sync()
			}
			parent.Close()
			if err != nil {
				return err
			}
		}
		if err = managedSyncDirectories(int(root.Fd())); err != nil {
			return err
		}
		if err = s.control.Sync(); err != nil {
			return err
		}
		db.Workspaces[name] = managedRecord{Workspace: w}
		return s.save(db)
	})
	if err != nil {
		return ManagedWorkspace{}, err
	}
	return w, nil
}

func managedSyncDirectories(fd int) error {
	dir, err := candidateDirectory(fd, ".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(4097)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > 4096 {
		return domain.ErrWriteDenied
	}
	for _, entry := range entries {
		if entry.IsDir() {
			child, err := candidateDirectory(fd, entry.Name())
			if err != nil {
				return err
			}
			err = managedSyncDirectories(int(child.Fd()))
			child.Close()
			if err != nil {
				return err
			}
		}
	}
	return dir.Sync()
}

// ReportWritableExposure permanently revokes eligibility; it is never reset.
func (s *ManagedWorkspaceStore) ReportWritableExposure(ctx context.Context, w ManagedWorkspace) error {
	return s.exclusive(ctx, func(db *managedDatabase) error {
		r, ok := db.Workspaces[w.WorkspaceID]
		if !ok || r.Workspace != w {
			return domain.ErrWriteDenied
		}
		r.Exposed = true
		db.Workspaces[w.WorkspaceID] = r
		return s.save(db)
	})
}
func (s *ManagedWorkspaceStore) Issue(ctx context.Context, a domain.WriteApproval) error {
	return s.exclusive(ctx, func(db *managedDatabase) error {
		if a.Validate() != nil {
			return domain.ErrWriteDenied
		}
		for _, r := range db.Approvals {
			if r.Approval.ApprovalID == a.ApprovalID || r.Approval.Nonce == a.Nonce {
				return ports.ErrWriteApprovalExists
			}
		}
		db.Approvals[a.ApprovalID] = domain.WriteApprovalRecord{Approval: a.Clone(), State: domain.WriteAvailable}
		return s.save(db)
	})
}
func (s *ManagedWorkspaceStore) Find(ctx context.Context, id domain.ApprovalID) (domain.WriteApprovalRecord, bool, error) {
	var out domain.WriteApprovalRecord
	var found bool
	err := s.exclusive(ctx, func(db *managedDatabase) error { out, found = db.Approvals[id]; out = out.Clone(); return nil })
	return out, found, err
}
func (s *ManagedWorkspaceStore) Reserve(ctx context.Context, a domain.WriteApproval, tx string, now time.Time) (ports.WriteReservation, error) {
	var out ports.WriteReservation
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		r, ok := db.Approvals[a.ApprovalID]
		if !ok {
			return ports.ErrWriteApprovalNotFound
		}
		if !r.Approval.Equal(a) {
			return domain.ErrWriteDenied
		}
		updated, granted, err := r.Reserve(tx, now)
		if err != nil {
			return err
		}
		if granted {
			for _, other := range db.Approvals {
				if other.TransactionID == tx {
					return domain.ErrWriteDenied
				}
			}
			db.Approvals[a.ApprovalID] = updated
			if err = s.save(db); err != nil {
				return err
			}
		}
		out = ports.WriteReservation{Record: updated.Clone(), Granted: granted}
		return nil
	})
	return out, err
}

// Finish cannot claim APPLIED without a terminal journal written by this store.
func (s *ManagedWorkspaceStore) Finish(ctx context.Context, id domain.ApprovalID, tx string, result domain.WriteTerminalResult) (domain.WriteApprovalRecord, error) {
	var out domain.WriteApprovalRecord
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		j, ok := db.Journals[tx]
		if !ok || j.ApprovalID != id || j.State != string(result.State) {
			return domain.ErrWriteDenied
		}
		r, ok := db.Approvals[id]
		if !ok {
			return ports.ErrWriteApprovalNotFound
		}
		var err error
		out, err = r.Finish(tx, result)
		if err != nil {
			return err
		}
		db.Approvals[id] = out
		return s.save(db)
	})
	return out.Clone(), err
}
func mediatedParametersIdentity(p domain.CandidatePolicy) string {
	p = p.Clone()
	sort.Strings(p.InputTargets)
	sort.Strings(p.WriteTargets)
	sort.Strings(p.ExcludedTargets)
	b, _ := json.Marshal(p)
	return domain.CandidateDigest(b)
}

// Check private directory ownership and aliases component by component. Reads
// still use O_NOFOLLOW regular-file descriptors and reject hardlinks/devices.
func managedSnapshot(ctx context.Context, root int, p domain.CandidatePolicy) (map[string]domain.CandidateFile, error) {
	if err := candidateGitAbsent(root); err != nil {
		return nil, err
	}
	for _, target := range p.Targets() {
		dir, err := candidateDirectory(root, ".")
		if err != nil {
			return nil, err
		}
		parts := strings.Split(target, "/")
		for i, part := range parts {
			var st unix.Stat_t
			if unix.Fstat(int(dir.Fd()), &st) != nil || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
				dir.Close()
				return nil, domain.ErrWriteDenied
			}
			listing, err := candidateDirectory(int(dir.Fd()), ".")
			if err != nil {
				dir.Close()
				return nil, err
			}
			names, err := listing.ReadDir(4097)
			listing.Close()
			if (err != nil && err != io.EOF) || len(names) > 4096 {
				dir.Close()
				return nil, domain.ErrWriteDenied
			}
			for _, name := range names {
				if strings.EqualFold(name.Name(), part) && name.Name() != part {
					dir.Close()
					return nil, domain.ErrWriteDenied
				}
			}
			if i == len(parts)-1 {
				break
			}
			next, err := candidateDirectory(int(dir.Fd()), part)
			dir.Close()
			if err != nil {
				return nil, err
			}
			dir = next
			if candidateGitAbsent(int(dir.Fd())) != nil {
				dir.Close()
				return nil, domain.ErrWriteDenied
			}
		}
		dir.Close()
	}
	return candidateSnapshot(ctx, root, p)
}
