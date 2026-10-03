package domain

import (
	"errors"
	"strings"
)

type ApprovalID string
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "PENDING"
	ApprovalApproved ApprovalStatus = "APPROVED"
	ApprovalRejected ApprovalStatus = "REJECTED"
)

var (
	ErrApprovalIDRequired        = errors.New("approval ID is required")
	ErrApprovalProjectIDRequired = errors.New("approval project ID is required")
	ErrApprovalTaskIDInvalid     = errors.New("approval task ID is invalid")
	ErrApprovalActionRequired    = errors.New("approval action is required")
	ErrApprovalNotPending        = errors.New("approval is not pending")
)

type Approval struct {
	ID        ApprovalID
	ProjectID ProjectID
	TaskID    *TaskID
	Action    string
	Status    ApprovalStatus
}

func NewApproval(id ApprovalID, projectID ProjectID, taskID *TaskID, action string) (Approval, error) {
	if strings.TrimSpace(string(id)) == "" {
		return Approval{}, ErrApprovalIDRequired
	}
	if strings.TrimSpace(string(projectID)) == "" {
		return Approval{}, ErrApprovalProjectIDRequired
	}
	if taskID != nil && strings.TrimSpace(string(*taskID)) == "" {
		return Approval{}, ErrApprovalTaskIDInvalid
	}
	if strings.TrimSpace(action) == "" {
		return Approval{}, ErrApprovalActionRequired
	}
	return Approval{ID: id, ProjectID: projectID, TaskID: taskID, Action: action, Status: ApprovalPending}, nil
}
func (a Approval) Approve() (Approval, error) {
	if a.Status != ApprovalPending {
		return Approval{}, ErrApprovalNotPending
	}
	a.Status = ApprovalApproved
	return a, nil
}
func (a Approval) Reject() (Approval, error) {
	if a.Status != ApprovalPending {
		return Approval{}, ErrApprovalNotPending
	}
	a.Status = ApprovalRejected
	return a, nil
}
