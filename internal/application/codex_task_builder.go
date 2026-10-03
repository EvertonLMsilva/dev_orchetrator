package application

import (
	"errors"
	"strings"
	"time"
	"unicode"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrInvalidExecutorTaskSpec = errors.New("invalid executor task spec")

// ExecutorTaskSpec describes authorized work. All text is declarative, never
// interpreted as commands. Scope contains canonical relative paths, not globs.
type ExecutorTaskSpec struct {
	Objective          string
	Scope              []string
	Constraints        []string
	AcceptanceCriteria []string
}

func (s ExecutorTaskSpec) Validate() error {
	if strings.TrimSpace(s.Objective) == "" || len(s.Scope) == 0 || len(s.AcceptanceCriteria) == 0 {
		return ErrInvalidExecutorTaskSpec
	}
	for _, p := range s.Scope {
		if !safeExecutorScopePath(p) {
			return ErrInvalidExecutorTaskSpec
		}
	}
	for _, items := range [][]string{s.Constraints, s.AcceptanceCriteria} {
		for _, item := range items {
			if strings.TrimSpace(item) == "" {
				return ErrInvalidExecutorTaskSpec
			}
		}
	}
	return nil
}

// Use a platform-independent slash-only grammar. Reject rather than clean paths
// so traversal, Windows roots/streams and patterns cannot broaden authority.
// This lexical contract neither accesses files nor resolves symlinks.
func safeExecutorScopePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:*?\"<>|") {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}

// CodexTask is the concrete CODEX_TASK payload. Envelope owns its identities.
type CodexTask struct {
	Spec ExecutorTaskSpec
}

func (t CodexTask) Validate() error { return t.Spec.Validate() }

// CodexTaskMetadata is explicit caller input; the builder creates no IDs or clock.
type CodexTaskMetadata struct {
	ProtocolVersion string
	MessageID       domain.MessageID
	CorrelationID   domain.CorrelationID
	SessionID       *domain.SessionID
	CreatedAt       time.Time
}

type CodexTaskRequest struct {
	Decision ports.PlannerDecision
	Spec     ExecutorTaskSpec
	Metadata CodexTaskMetadata
}

type CodexTaskBuilder struct{}

// Build only prepares a contract. It does not execute work or change task state.
// Every failure returns an empty envelope.
func (CodexTaskBuilder) Build(r CodexTaskRequest) (domain.Envelope, error) {
	if err := r.Decision.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	if r.Decision.Type != ports.PlannerDecisionPrepareExecutor {
		return domain.Envelope{}, ports.ErrInvalidPlannerDecision
	}
	if err := r.Spec.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	// Copy collections so caller mutations cannot change the prepared authority.
	spec := r.Spec
	spec.Scope = append([]string(nil), spec.Scope...)
	spec.Constraints = append([]string(nil), spec.Constraints...)
	spec.AcceptanceCriteria = append([]string(nil), spec.AcceptanceCriteria...)
	taskID := r.Decision.TaskID
	envelope := domain.Envelope{
		ProtocolVersion: r.Metadata.ProtocolVersion,
		MessageType:     domain.MessageTypeCodexTask,
		MessageID:       r.Metadata.MessageID,
		CorrelationID:   r.Metadata.CorrelationID,
		ProjectID:       r.Decision.ProjectID,
		TaskID:          &taskID,
		CreatedAt:       r.Metadata.CreatedAt,
		Payload:         CodexTask{Spec: spec},
	}
	if r.Metadata.SessionID != nil {
		sessionID := *r.Metadata.SessionID
		envelope.SessionID = &sessionID
	}
	if err := envelope.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	return envelope, nil
}
