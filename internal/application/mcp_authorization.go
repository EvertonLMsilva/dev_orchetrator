package application

import (
	"context"
	"errors"
	"strings"

	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var errMCPAccess = errors.New("MCP access denied or unavailable")

// mcpAuthorization is private authentication/grant policy. Entry compositions
// must use SecureReadRuntime to require audit before READ and result delivery.
type mcpAuthorization struct {
	authentication ports.AuthenticationPort
	grants         ports.GrantRepository
	projects       ports.ProjectRepository
	read           *readBoundary
}

func newMCPAuthorization(a ports.AuthenticationPort, g ports.GrantRepository, p ports.ProjectRepository, r *readBoundary) *mcpAuthorization {
	return &mcpAuthorization{authentication: a, grants: g, projects: p, read: r}
}

func (b *mcpAuthorization) dispatch(ctx context.Context, evidence ports.AuthenticationEvidence, operation string, request any) (any, error) {
	_, _, err := b.authorize(ctx, evidence, operation, request)
	if err != nil {
		return nil, err
	}
	return b.read.dispatch(ctx, operation, request)
}

// authorize is shared by the private MCP-3 boundary and the audited runtime.
// It never invokes READ, allowing durable audit to gate that invocation.
func (b *mcpAuthorization) authorize(ctx context.Context, evidence ports.AuthenticationEvidence, operation string, request any) (principal ports.Principal, failure string, err error) {
	failure = "AUTHENTICATION_DENIED"
	if err := ctx.Err(); err != nil {
		return ports.Principal{}, failure, err
	}
	if b == nil || b.authentication == nil || b.grants == nil || b.projects == nil || b.read == nil || len(evidence.Material) == 0 {
		return ports.Principal{}, failure, errMCPAccess
	}
	principal, err = b.authentication.Authenticate(ctx, evidence)
	if err != nil || strings.TrimSpace(principal.ID) == "" || len(principal.ID) > readcontracts.MaxIDBytes || strings.ContainsAny(principal.ID, "\x00\r\n") {
		return ports.Principal{}, failure, errMCPAccess
	}
	if err := ctx.Err(); err != nil {
		return principal, failure, err
	}
	failure = "AUTHORIZATION_DENIED"
	var projectID string
	switch operation {
	case "project.status":
		r, ok := request.(readcontracts.ProjectStatusRequest)
		if !ok || r.Validate() != nil {
			return principal, failure, errMCPAccess
		}
		projectID = r.ProjectID
	case "project.tasks":
		r, ok := request.(readcontracts.ProjectTasksRequest)
		if !ok || r.Validate() != nil {
			return principal, failure, errMCPAccess
		}
		projectID = r.ProjectID
	case "git.status":
		r, ok := request.(readcontracts.GitStatusRequest)
		if !ok || r.Validate() != nil {
			return principal, failure, errMCPAccess
		}
		projectID = r.ProjectID
	case "execution.status":
		r, ok := request.(readcontracts.ExecutionStatusRequest)
		if !ok || r.Validate() != nil {
			return principal, failure, errMCPAccess
		}
		projectID = r.ProjectID
	default:
		return principal, failure, errMCPAccess
	}
	wanted := ports.Grant{PrincipalID: principal.ID, Operation: operation, ProjectID: projectID}
	grant, found, err := b.grants.FindExact(ctx, wanted)
	if err != nil || !found || grant != wanted {
		return principal, failure, errMCPAccess
	}
	if err := ctx.Err(); err != nil {
		return principal, failure, err
	}
	project, found, err := b.projects.FindByID(ctx, domain.ProjectID(projectID))
	if err != nil || !found || string(project.ID) != projectID {
		return principal, failure, errMCPAccess
	}
	if err := ctx.Err(); err != nil {
		return principal, failure, err
	}
	return principal, "", nil
}
