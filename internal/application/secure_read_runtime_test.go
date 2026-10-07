package application

import (
	"context"
	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"testing"
)

type readAudit struct {
	events  []ports.ReadAuditEvent
	failAt  int
	backend *readProjects
}

func (a *readAudit) Append(_ context.Context, e ports.ReadAuditEvent) error {
	a.events = append(a.events, e)
	if e.Outcome == "AUTHORIZED" && a.backend.calls != 1 {
		return errors.New("READ executed before authorization audit")
	}
	if a.failAt == len(a.events) {
		return errors.New("raw secret error")
	}
	return nil
}
func TestSecureReadRuntimeAudit(t *testing.T) {
	for _, scenario := range []string{"success", "auth denied", "auth absent", "grant denied", "grant failure", "project absent", "read failure", "audit received failure", "audit authorization failure", "audit result failure", "audit denial failure", "nil audit", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			p := &readProjects{project: domain.Project{ID: "p", Name: "P"}}
			a := &mcpAuthentication{principal: ports.Principal{ID: "user"}}
			g := &mcpGrants{grant: ports.Grant{PrincipalID: "user", Operation: "project.tasks", ProjectID: "p"}, found: true}
			tasks := &readTasks{}
			audit := &readAudit{backend: p}
			evidence := ports.AuthenticationEvidence{Material: []byte("DO-NOT-RECORD")}
			ctx := context.Background()
			switch scenario {
			case "auth denied":
				a.err = errors.New("DO-NOT-RECORD")
			case "auth absent":
				evidence.Material = nil
			case "grant denied":
				g.found = false
			case "grant failure":
				g.err = errors.New("DO-NOT-RECORD")
			case "project absent":
				p.project = domain.Project{}
			case "read failure":
				tasks.err = errors.New("DO-NOT-RECORD")
			case "audit received failure":
				audit.failAt = 1
			case "audit authorization failure":
				audit.failAt = 2
			case "audit result failure":
				audit.failAt = 3
			case "audit denial failure":
				g.found = false
				audit.failAt = 2
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var sink ports.ReadAudit = audit
			if scenario == "nil audit" {
				sink = nil
			}
			r := NewSecureReadRuntime(a, g, sink, p, tasks, nil)
			out, err := r.Query(ctx, evidence, "project.tasks", readcontracts.ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: 1})
			if scenario == "success" {
				if err != nil || out == nil {
					t.Fatal(out, err)
				}
			} else if err == nil || out != nil {
				t.Fatal("failure exposed result", out, err)
			}
			want := "SUCCESS"
			switch scenario {
			case "auth denied", "auth absent", "cancelled":
				want = "AUTHENTICATION_DENIED"
			case "grant denied", "grant failure", "project absent":
				want = "AUTHORIZATION_DENIED"
			case "read failure":
				want = "READ_FAILURE"
			}
			if scenario == "nil audit" {
				if a.calls != 0 || p.calls != 0 {
					t.Fatal("missing audit bypass")
				}
				return
			}
			if audit.failAt > 0 {
				if scenario == "audit denial failure" && p.calls != 0 {
					t.Fatal("failed denial audit bypass")
				}
				if scenario == "audit received failure" && (a.calls != 0 || p.calls != 0) {
					t.Fatal("initial audit bypass")
				}
				if scenario == "audit authorization failure" && p.calls != 1 {
					t.Fatal("READ bypass")
				}
				return
			}
			if len(audit.events) < 2 || audit.events[0].Outcome != "RECEIVED" || audit.events[len(audit.events)-1].Outcome != want {
				t.Fatal(audit.events)
			}
			for _, event := range audit.events {
				if event.CorrelationID != "c" || event.ProjectID != "p" || event.Operation != "project.tasks" || event.Timestamp.IsZero() {
					t.Fatal(event)
				}
			}
			if want == "AUTHENTICATION_DENIED" && audit.events[len(audit.events)-1].PrincipalID != "" {
				t.Fatal("unvalidated principal audited")
			}
			if (want == "AUTHENTICATION_DENIED" || scenario == "grant denied" || scenario == "grant failure") && p.calls != 0 {
				t.Fatal("denied READ bypass")
			}
		})
	}
}
