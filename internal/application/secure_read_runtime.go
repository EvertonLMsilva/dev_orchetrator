package application

import (
	"context"
	"crypto/rand"
	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// SecureReadRuntime exposes only authenticated, authorized and audited READ.
// Construction belongs to trusted internal composition, never to the Channel.
// Its private fields do not expose the underlying READ boundary.
type SecureReadRuntime struct {
	security *mcpAuthorization
	audit    ports.ReadAudit
}

func NewSecureReadRuntime(a ports.AuthenticationPort, g ports.GrantRepository, audit ports.ReadAudit, p ports.ProjectRepository, t ports.TaskRepository, agent *LocalAgentDispatcher) *SecureReadRuntime {
	return &SecureReadRuntime{security: newMCPAuthorization(a, g, p, newReadBoundary(p, t, agent)), audit: audit}
}

func (r *SecureReadRuntime) Query(ctx context.Context, evidence ports.AuthenticationEvidence, operation string, request any) (any, error) {
	if r == nil || r.audit == nil || r.security == nil {
		return nil, errMCPAccess
	}
	event := auditMetadata(operation, request)
	var attempt [16]byte
	if _, err := rand.Read(attempt[:]); err != nil {
		return nil, errMCPAccess
	}
	event.AttemptID = hex.EncodeToString(attempt[:])
	// Audit even cancelled/denied attempts with an independent bounded context.
	appendEvent := func(outcome, errorClass string) error {
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		event.Timestamp = time.Now().UTC()
		event.Outcome = outcome
		event.ErrorClass = errorClass
		if r.audit.Append(auditCtx, event) != nil {
			return errMCPAccess
		}
		return nil
	}
	if appendEvent("RECEIVED", "") != nil {
		return nil, errMCPAccess
	}
	principal, failure, err := r.security.authorize(ctx, evidence, operation, request)
	event.PrincipalID = principal.ID
	if err != nil {
		if appendEvent(failure, "ACCESS_DENIED") != nil {
			return nil, errMCPAccess
		}
		return nil, errMCPAccess
	}
	if appendEvent("AUTHORIZED", "") != nil {
		return nil, errMCPAccess
	}
	result, err := r.security.read.dispatch(ctx, operation, request)
	if err != nil {
		if appendEvent("READ_FAILURE", "READ_UNAVAILABLE") != nil {
			return nil, errMCPAccess
		}
		return nil, errMCPAccess
	}
	if appendEvent("SUCCESS", "") != nil {
		return nil, errMCPAccess
	}
	if ctx.Err() != nil {
		return nil, errMCPAccess
	}
	return result, nil
}

func auditMetadata(operation string, request any) ports.ReadAuditEvent {
	e := ports.ReadAuditEvent{Operation: "UNKNOWN"}
	switch operation {
	case "project.status", "project.tasks", "git.status", "execution.status":
		e.Operation = operation
	}
	switch r := request.(type) {
	case readcontracts.ProjectStatusRequest:
		e.ProjectID, e.CorrelationID = r.ProjectID, r.CorrelationID
	case readcontracts.ProjectTasksRequest:
		e.ProjectID, e.CorrelationID = r.ProjectID, r.CorrelationID
	case readcontracts.GitStatusRequest:
		e.ProjectID, e.CorrelationID = r.ProjectID, r.CorrelationID
	case readcontracts.ExecutionStatusRequest:
		e.ProjectID, e.CorrelationID = r.ProjectID, r.CorrelationID
	}
	safe := func(s string) string {
		if len(s) > readcontracts.MaxIDBytes || strings.TrimSpace(s) != s || s == "" || !utf8.ValidString(s) || strings.ContainsAny(s, "*\x00\r\n") {
			return ""
		}
		return s
	}
	e.ProjectID = safe(e.ProjectID)
	e.CorrelationID = safe(e.CorrelationID)
	return e
}
