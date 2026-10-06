package domain

import (
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type WriteOperationKind string

const WriteApply WriteOperationKind = "WRITE_APPLY"

var (
	ErrInvalidWriteApproval = errors.New("invalid write approval")
	ErrWriteDenied          = errors.New("write authorization denied")
	ErrWriteStateConflict   = errors.New("write consumption state conflict")
)

type WriteBinding struct {
	ProjectID                   ProjectID
	TaskID                      TaskID
	CorrelationID               string
	WorkspaceIdentity           string
	BaseIdentity                string
	OperationKind               WriteOperationKind
	AllowedTargets              []string
	ApprovedDiffIdentity        string
	ApprovedArtifactID          string
	ExpectedPostIdentity        string
	OperationParametersIdentity string
	PolicyVersion               string
	Nonce                       string
}

func (b WriteBinding) Clone() WriteBinding {
	b.AllowedTargets = append([]string(nil), b.AllowedTargets...)
	return b
}
func (b WriteBinding) Validate() error {
	if b.OperationKind != WriteApply {
		return ErrInvalidWriteApproval
	}
	for _, value := range []string{string(b.ProjectID), string(b.TaskID), b.CorrelationID, b.WorkspaceIdentity, b.ApprovedArtifactID, b.PolicyVersion, b.Nonce} {
		if !writeLabel(value) {
			return ErrInvalidWriteApproval
		}
	}
	for _, value := range []string{b.BaseIdentity, b.ApprovedDiffIdentity, b.ExpectedPostIdentity, b.OperationParametersIdentity} {
		if !validWriteDigest(value) {
			return ErrInvalidWriteApproval
		}
	}
	if _, err := canonicalWriteTargets(b.AllowedTargets); err != nil {
		return ErrInvalidWriteApproval
	}
	return nil
}
func (b WriteBinding) Equal(other WriteBinding) bool {
	if b.Validate() != nil || other.Validate() != nil {
		return false
	}
	b.AllowedTargets, _ = canonicalWriteTargets(b.AllowedTargets)
	other.AllowedTargets, _ = canonicalWriteTargets(other.AllowedTargets)
	left, _ := json.Marshal(b)
	right, _ := json.Marshal(other)
	return slices.Equal(left, right)
}

// Issuance is a trusted boundary: these fields do not authenticate an actor.
// This value is separate from the legacy symbolic Approval contract.
type WriteApproval struct {
	SchemaVersion int
	ApprovalID    ApprovalID
	WriteBinding
	ApproverIdentity string
	IssuedAt         time.Time
	ExpiresAt        time.Time
}

func (a WriteApproval) Clone() WriteApproval { a.WriteBinding = a.WriteBinding.Clone(); return a }
func (a WriteApproval) Validate() error {
	if a.SchemaVersion != WriteSchemaVersion || !writeLabel(string(a.ApprovalID)) || !writeLabel(a.ApproverIdentity) || a.IssuedAt.IsZero() || !a.ExpiresAt.After(a.IssuedAt) || a.WriteBinding.Validate() != nil {
		return ErrInvalidWriteApproval
	}
	return nil
}
func (a WriteApproval) Equal(other WriteApproval) bool {
	return a.Validate() == nil && other.Validate() == nil && a.SchemaVersion == other.SchemaVersion && a.ApprovalID == other.ApprovalID && a.ApproverIdentity == other.ApproverIdentity && a.IssuedAt.Equal(other.IssuedAt) && a.ExpiresAt.Equal(other.ExpiresAt) && a.WriteBinding.Equal(other.WriteBinding)
}
func (a WriteApproval) ValidAt(now time.Time) bool {
	return a.Validate() == nil && !now.Before(a.IssuedAt) && now.Before(a.ExpiresAt)
}

// expected is trusted request context. Matching candidate data alone is never
// enough; policy and nonce must also match the approval's exact context.
func ValidateWriteAuthorization(a WriteApproval, c WriteCandidate, expected WriteBinding, now time.Time) error {
	if !a.ValidAt(now) || expected.Validate() != nil || !a.WriteBinding.Equal(expected) {
		return ErrWriteDenied
	}
	actual, err := c.Binding(expected.PolicyVersion, expected.Nonce)
	if err != nil || !actual.Equal(expected) {
		return ErrWriteDenied
	}
	return nil
}

type WriteConsumptionState string

const (
	WriteAvailable        WriteConsumptionState = "AVAILABLE"
	WriteReserved         WriteConsumptionState = "RESERVED"
	WriteApplied          WriteConsumptionState = "APPLIED"
	WriteAborted          WriteConsumptionState = "ABORTED"
	WriteRecoveryRequired WriteConsumptionState = "RECOVERY_REQUIRED"
)

// ResultIdentity is an opaque SHA-256 receipt/post-state digest, not model text.
// A recovery result remains terminal here; journal recovery belongs to P10.3.
type WriteTerminalResult struct {
	State          WriteConsumptionState
	ResultIdentity string
}

func (r WriteTerminalResult) Validate() error {
	if !validWriteDigest(r.ResultIdentity) {
		return ErrWriteStateConflict
	}
	switch r.State {
	case WriteApplied, WriteAborted, WriteRecoveryRequired:
		return nil
	default:
		return ErrWriteStateConflict
	}
}

type WriteApprovalRecord struct {
	Approval      WriteApproval
	State         WriteConsumptionState
	TransactionID string
	ReservedAt    time.Time
	Result        WriteTerminalResult
}

func (r WriteApprovalRecord) Clone() WriteApprovalRecord { r.Approval = r.Approval.Clone(); return r }
func (r WriteApprovalRecord) Validate() error {
	if r.Approval.Validate() != nil {
		return ErrWriteStateConflict
	}
	switch r.State {
	case WriteAvailable:
		if r.TransactionID != "" || !r.ReservedAt.IsZero() || r.Result != (WriteTerminalResult{}) {
			return ErrWriteStateConflict
		}
	case WriteReserved:
		if !writeLabel(r.TransactionID) || !r.Approval.ValidAt(r.ReservedAt) || r.Result != (WriteTerminalResult{}) {
			return ErrWriteStateConflict
		}
	case WriteApplied, WriteAborted, WriteRecoveryRequired:
		if !writeLabel(r.TransactionID) || !r.Approval.ValidAt(r.ReservedAt) || r.Result.Validate() != nil || r.Result.State != r.State {
			return ErrWriteStateConflict
		}
		if r.State == WriteApplied && r.Result.ResultIdentity != r.Approval.ExpectedPostIdentity {
			return ErrWriteStateConflict
		}
	default:
		return ErrWriteStateConflict
	}
	return nil
}
func (r WriteApprovalRecord) Reserve(transaction string, now time.Time) (WriteApprovalRecord, bool, error) {
	if r.Validate() != nil || !writeLabel(transaction) {
		return WriteApprovalRecord{}, false, ErrWriteStateConflict
	}
	if r.State != WriteAvailable {
		return r.Clone(), false, nil
	}
	if !r.Approval.ValidAt(now) {
		return WriteApprovalRecord{}, false, ErrWriteDenied
	}
	r = r.Clone()
	r.State = WriteReserved
	r.TransactionID = transaction
	r.ReservedAt = now
	return r, true, nil
}
func (r WriteApprovalRecord) Finish(transaction string, result WriteTerminalResult) (WriteApprovalRecord, error) {
	if r.Validate() != nil || r.State == WriteAvailable || transaction != r.TransactionID || result.Validate() != nil {
		return WriteApprovalRecord{}, ErrWriteStateConflict
	}
	if r.State != WriteReserved {
		if r.Result == result {
			return r.Clone(), nil
		}
		return WriteApprovalRecord{}, ErrWriteStateConflict
	}
	if result.State == WriteApplied && result.ResultIdentity != r.Approval.ExpectedPostIdentity {
		return WriteApprovalRecord{}, ErrWriteStateConflict
	}
	r = r.Clone()
	r.State = result.State
	r.Result = result
	return r, nil
}
