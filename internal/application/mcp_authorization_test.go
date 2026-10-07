package application

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type mcpAuthentication struct {
	principal ports.Principal
	err       error
	calls     int
}

func (a *mcpAuthentication) Authenticate(context.Context, ports.AuthenticationEvidence) (ports.Principal, error) {
	a.calls++
	return a.principal, a.err
}

type mcpGrants struct {
	grant   ports.Grant
	found   bool
	err     error
	calls   int
	queried ports.Grant
}

func (g *mcpGrants) FindExact(_ context.Context, wanted ports.Grant) (ports.Grant, bool, error) {
	g.calls++
	g.queried = wanted
	return g.grant, g.found, g.err
}

func TestMCPAuthorization(t *testing.T) {
	for _, operation := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		t.Run(operation, func(t *testing.T) {
			p := &readProjects{project: domain.Project{ID: "p", Name: "P", Workspace: "root"}}
			a := &mcpAuthentication{principal: ports.Principal{ID: "user"}}
			g := &mcpGrants{grant: ports.Grant{PrincipalID: "user", Operation: operation, ProjectID: "p"}, found: true}
			git := &readGit{}
			registry := &readProjects{project: p.project}
			b := newMCPAuthorization(a, g, registry, newReadBoundary(p, &readTasks{}, NewLocalAgent(p, nil, NewReadOnlyActionAllowlist(), git)))
			var req any = readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"}
			switch operation {
			case "project.tasks":
				req = readcontracts.ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: 1}
			case "git.status":
				req = readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "c"}
			case "execution.status":
				req = readcontracts.ExecutionStatusRequest{ProjectID: "p", CorrelationID: "c"}
			}
			for i := 0; i < 2; i++ {
				if out, err := b.dispatch(context.Background(), ports.AuthenticationEvidence{Material: []byte("proof")}, operation, req); err != nil || out == nil {
					t.Fatal(out, err)
				}
			}
			if a.calls != 2 || g.calls != 2 || g.queried != g.grant || registry.calls != 2 {
				t.Fatal("authentication, exact grants and project resolution required per request", a.calls, g.calls, registry.calls)
			}
		})
	}
}

func TestMCPAuthorizationDenyBeforeRead(t *testing.T) {
	for _, scenario := range []string{"missing evidence", "authentication failure", "missing principal", "invalid principal", "grant failure", "missing grant", "other principal", "other operation", "other project", "wildcard", "missing project", "project failure", "project mismatch", "missing authentication", "missing grants", "missing registry", "missing read", "unknown operation", "wrong request", "invalid request", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			a := &mcpAuthentication{principal: ports.Principal{ID: "user"}}
			g := &mcpGrants{grant: ports.Grant{PrincipalID: "user", Operation: "project.status", ProjectID: "p"}, found: true}
			registry := &readProjects{project: domain.Project{ID: "p", Name: "P"}}
			backend := &readProjects{project: domain.Project{ID: "p", Name: "P"}}
			b := newMCPAuthorization(a, g, registry, newReadBoundary(backend, nil, nil))
			evidence := ports.AuthenticationEvidence{Material: []byte("proof")}
			operation := "project.status"
			var req any = readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"}
			ctx := context.Background()
			switch scenario {
			case "missing evidence":
				evidence = ports.AuthenticationEvidence{}
			case "authentication failure":
				a.err = errors.New("secret")
			case "missing principal":
				a.principal.ID = ""
			case "invalid principal":
				a.principal.ID = "\n"
			case "grant failure":
				g.err = errors.New("secret")
			case "missing grant":
				g.found = false
			case "other principal":
				g.grant.PrincipalID = "other"
			case "other operation":
				g.grant.Operation = "git.status"
			case "other project":
				g.grant.ProjectID = "other"
			case "wildcard":
				g.grant.ProjectID = "*"
			case "missing project":
				registry.project = domain.Project{}
			case "project failure":
				registry.err = errors.New("secret")
			case "project mismatch":
				registry.project.ID = "other"
			case "missing authentication":
				b.authentication = nil
			case "missing grants":
				b.grants = nil
			case "missing registry":
				b.projects = nil
			case "missing read":
				b.read = nil
			case "unknown operation":
				operation = "WRITE"
			case "wrong request":
				req = readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "c"}
			case "invalid request":
				req = readcontracts.ProjectStatusRequest{}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			out, err := b.dispatch(ctx, evidence, operation, req)
			if err == nil || out != nil || backend.calls != 0 {
				t.Fatal("denied request reached READ", out, err, backend.calls)
			}
			if scenario != "cancelled" && err != errMCPAccess {
				t.Fatal("failure leaked backend detail", err)
			}
			if (scenario == "authentication failure" || scenario == "missing principal" || scenario == "invalid principal" || scenario == "missing evidence") && g.calls != 0 {
				t.Fatal("unauthenticated grants lookup")
			}
			if (scenario == "grant failure" || scenario == "missing grant" || scenario == "other principal" || scenario == "other operation" || scenario == "other project" || scenario == "wildcard") && registry.calls != 0 {
				t.Fatal("unauthorized project lookup")
			}
		})
	}
}
