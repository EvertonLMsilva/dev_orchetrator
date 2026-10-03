package ports

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"dev-orchestrator/internal/domain"
)

// Executor executes authorized work using provider-independent contracts.
// Callers validate requests before Execute and results before consuming them,
// including matching ProjectID and TaskID to the originating request.
// DONE, BLOCKED and FAILED are protocol results; error indicates a boundary
// or infrastructure failure, not a FAILED outcome.
type Executor interface {
	Execute(context.Context, ExecutorRequest) (ExecutorResult, error)
}

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

var (
	ErrInvalidExecutorRequest = errors.New("invalid executor request")
	ErrInvalidExecutorResult  = errors.New("invalid executor result")
)

type ExecutorRequest struct {
	ProjectID domain.ProjectID
	TaskID    domain.TaskID
	Spec      ExecutorTaskSpec
}

func (r ExecutorRequest) Validate() error {
	if strings.TrimSpace(string(r.ProjectID)) == "" || strings.TrimSpace(string(r.TaskID)) == "" || r.Spec.Validate() != nil {
		return ErrInvalidExecutorRequest
	}
	return nil
}

type ExecutorOutcome string

const (
	ExecutorOutcomeDone    ExecutorOutcome = "DONE"
	ExecutorOutcomeBlocked ExecutorOutcome = "BLOCKED"
	ExecutorOutcomeFailed  ExecutorOutcome = "FAILED"
)

type ExecutorResult struct {
	ProjectID domain.ProjectID
	TaskID    domain.TaskID
	Outcome   ExecutorOutcome
	Summary   string
}

func (r ExecutorResult) Validate() error {
	if strings.TrimSpace(string(r.ProjectID)) == "" || strings.TrimSpace(string(r.TaskID)) == "" || strings.TrimSpace(r.Summary) == "" {
		return ErrInvalidExecutorResult
	}
	switch r.Outcome {
	case ExecutorOutcomeDone, ExecutorOutcomeBlocked, ExecutorOutcomeFailed:
		return nil
	default:
		return ErrInvalidExecutorResult
	}
}
