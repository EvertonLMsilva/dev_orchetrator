package domain

type SessionType string

const (
	SessionTypePlanner  SessionType = "PLANNER"
	SessionTypeExecutor SessionType = "EXECUTOR"
)

type SessionStartReason string

const (
	SessionStartReasonNewTask SessionStartReason = "NEW_TASK"
	SessionStartReasonHandoff SessionStartReason = "HANDOFF"
	SessionStartReasonInitial SessionStartReason = "INITIAL"
)

type SessionCloseReason string

const (
	SessionCloseReasonDone          SessionCloseReason = "DONE"
	SessionCloseReasonFailed        SessionCloseReason = "FAILED"
	SessionCloseReasonCancelled     SessionCloseReason = "CANCELLED"
	SessionCloseReasonContextBudget SessionCloseReason = "CONTEXT_BUDGET"
	SessionCloseReasonReplaced      SessionCloseReason = "REPLACED"
)

type SessionStartedPayload struct {
	SessionType SessionType
	SessionID   SessionID
	Reason      SessionStartReason
}

type SessionClosedPayload struct {
	SessionType SessionType
	SessionID   SessionID
	Reason      SessionCloseReason
}
