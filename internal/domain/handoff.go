package domain

type PlannerHandoffPayload struct {
	ProjectID   ProjectID
	CurrentTask *TaskID
	TaskStatus  *TaskStatus
	Decisions   []string
	Evidence    []string
	Completed   []TaskID
	Blocked     []TaskID
	NextAction  string
}
