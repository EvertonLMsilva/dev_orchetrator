package ports

import (
	"context"
	"time"
)

// ReadAuditEvent contains only bounded identifiers and fixed outcome/error classes.
// Empty PrincipalID means authentication has not established an identity.
// Empty request identifiers mean they were unavailable or invalid, not authorized.
type ReadAuditEvent struct {
	AttemptID     string    `json:"attemptId"`
	PrincipalID   string    `json:"principalId"`
	Operation     string    `json:"operation"`
	ProjectID     string    `json:"projectId"`
	CorrelationID string    `json:"correlationId"`
	Timestamp     time.Time `json:"timestamp"`
	Outcome       string    `json:"outcome"`
	ErrorClass    string    `json:"errorClass,omitempty"`
}

// ReadAudit must durably append the event before returning success. Failure denies
// further processing or delivery of a READ result. No raw evidence/errors allowed.
type ReadAudit interface {
	Append(context.Context, ReadAuditEvent) error
}
