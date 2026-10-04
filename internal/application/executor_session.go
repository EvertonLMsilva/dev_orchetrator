package application

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"dev-orchestrator/internal/domain"
)

var (
	ErrInvalidExecutorSession           = errors.New("invalid executor session")
	ErrInvalidExecutorSessionTransition = errors.New("invalid executor session transition")
)

type ExecutorSessionState string

const (
	ExecutorSessionActive    ExecutorSessionState = "ACTIVE"
	ExecutorSessionCompleted ExecutorSessionState = "COMPLETED"
	ExecutorSessionFailed    ExecutorSessionState = "FAILED"
	ExecutorSessionCancelled ExecutorSessionState = "CANCELLED"
)

// ExecutorSession is one disposable attempt, independent of task state and
// protocol outcomes. Private fields protect identity and terminal states.
// Like PlannerSession, transitions return a new value that callers must retain.
type ExecutorSession struct {
	sessionID domain.SessionID
	projectID domain.ProjectID
	taskID    domain.TaskID
	state     ExecutorSessionState
}

func (s ExecutorSession) SessionID() domain.SessionID { return s.sessionID }
func (s ExecutorSession) ProjectID() domain.ProjectID { return s.projectID }
func (s ExecutorSession) TaskID() domain.TaskID       { return s.taskID }
func (s ExecutorSession) State() ExecutorSessionState { return s.state }

func (s ExecutorSession) Validate() error {
	if strings.TrimSpace(string(s.sessionID)) == "" || strings.TrimSpace(string(s.projectID)) == "" || strings.TrimSpace(string(s.taskID)) == "" {
		return ErrInvalidExecutorSession
	}
	switch s.state {
	case ExecutorSessionActive, ExecutorSessionCompleted, ExecutorSessionFailed, ExecutorSessionCancelled:
		return nil
	default:
		return ErrInvalidExecutorSession
	}
}

// Complete closes a protocol-valid execution, including BLOCKED/FAILED outcomes.
func (s ExecutorSession) Complete() (ExecutorSession, error) {
	return s.transition(ExecutorSessionCompleted)
}

// Fail closes an attempt after a runtime, protocol or execution boundary error.
func (s ExecutorSession) Fail() (ExecutorSession, error) { return s.transition(ExecutorSessionFailed) }

// Cancel closes an attempt after context or user cancellation; it adds no outcome.
func (s ExecutorSession) Cancel() (ExecutorSession, error) {
	return s.transition(ExecutorSessionCancelled)
}

func (s ExecutorSession) transition(next ExecutorSessionState) (ExecutorSession, error) {
	if err := s.Validate(); err != nil {
		return s, err
	}
	if s.state != ExecutorSessionActive {
		return s, ErrInvalidExecutorSessionTransition
	}
	switch next {
	case ExecutorSessionCompleted, ExecutorSessionFailed, ExecutorSessionCancelled:
		s.state = next
		return s, nil
	default:
		return s, ErrInvalidExecutorSessionTransition
	}
}

// ExecutorSessionIDGenerator must return a fresh, provider-independent ID per
// call. Errors propagate without creating a session.
type ExecutorSessionIDGenerator func() (domain.SessionID, error)

// ExecutorSessionManager creates attempts without persistence or retry policy.
type ExecutorSessionManager struct{ generate ExecutorSessionIDGenerator }

// NewExecutorSessionManager uses cryptographically random IDs when generate is nil.
func NewExecutorSessionManager(generate ExecutorSessionIDGenerator) *ExecutorSessionManager {
	if generate == nil {
		generate = generateExecutorSessionID
	}
	return &ExecutorSessionManager{generate: generate}
}

func (m *ExecutorSessionManager) Create(projectID domain.ProjectID, taskID domain.TaskID) (ExecutorSession, error) {
	if strings.TrimSpace(string(projectID)) == "" || strings.TrimSpace(string(taskID)) == "" || m == nil || m.generate == nil {
		return ExecutorSession{}, ErrInvalidExecutorSession
	}
	id, err := m.generate()
	if err != nil {
		return ExecutorSession{}, err
	}
	s := ExecutorSession{sessionID: id, projectID: projectID, taskID: taskID, state: ExecutorSessionActive}
	if err := s.Validate(); err != nil {
		return ExecutorSession{}, err
	}
	return s, nil
}

func generateExecutorSessionID() (domain.SessionID, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return domain.SessionID(hex.EncodeToString(id[:])), nil
}
