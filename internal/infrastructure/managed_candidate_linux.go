//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"path/filepath"
)

type managedCandidateFactory struct {
	store     *ManagedWorkspaceStore
	workspace ManagedWorkspace
	scratch   string
}

// Trusted snapshot adapter: no managed path or descriptor crosses into a
// generator. The existing independent-copy writer remains the only candidate
// writer. Registry validation and snapshot copying are serialized with writers.
func NewManagedCandidateWorkspaceFactory(s *ManagedWorkspaceStore, w ManagedWorkspace, scratch string) ports.CandidateWorkspaceFactory {
	return &managedCandidateFactory{s, w, scratch}
}
func (f *managedCandidateFactory) Prepare(ctx context.Context, source, identity string, p domain.CandidatePolicy) (ports.CandidateWorkspace, error) {
	if f == nil || f.store == nil || source != "" || identity != f.workspace.Identity() {
		return nil, domain.ErrCandidateDenied
	}
	var copy *candidateWorkspace
	err := f.store.exclusive(ctx, func(db *managedDatabase) error {
		root, err := f.store.openWorkspace(db, f.workspace)
		if err != nil {
			return err
		}
		defer root.Close()
		if db.Workspaces[f.workspace.WorkspaceID].Lease != nil {
			return ErrManagedWriteBlocked
		}
		before, err := managedSnapshot(ctx, int(root.Fd()), p)
		if err != nil {
			return err
		}
		factory := &candidateWorkspaceFactory{scratchRoot: f.scratch}
		prepared, err := factory.Prepare(ctx, filepath.Join(f.store.controlPath, f.workspace.WorkspaceID), identity, p)
		if prepared != nil {
			copy = prepared.(*candidateWorkspace)
		}
		if err != nil {
			return err
		}
		if copy.base != candidateStateIdentity(p, before) {
			return domain.ErrCandidateDenied
		}
		// Identity originates in the authenticated managed registry, not caller data.
		copy.workspace = f.workspace.Identity()
		return nil
	})
	if copy == nil {
		return nil, err
	}
	return copy, err
}
