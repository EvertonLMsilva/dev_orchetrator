package ports

// PlannerWriteOperation is a symbolic proposal bound by value to its context.
// Paths use canonical absolute slash syntax; no filesystem is inspected.
type PlannerWriteOperation struct {
	ProjectID string
	TaskID    string
	Workspace string
	Kind      string
	Target    string
}

// PlannerWriteReview approves exactly Operation in the stated context.
// Its zero value grants nothing. ALLOW is symbolic authorization only:
// it does not authorize real WRITE or mediate effects produced by Codex.
type PlannerWriteReview struct {
	Boundary  string
	Decision  string
	ProjectID string
	TaskID    string
	Workspace string
	Operation PlannerWriteOperation
}

// WriteExecutor is a symbolic sink, not a real effect executor.
// P6.3d supplies no filesystem, Git, network, provider or Codex integration.
// Real WRITE remains DENY.
type WriteExecutor interface {
	Execute(PlannerWriteOperation) error
}
