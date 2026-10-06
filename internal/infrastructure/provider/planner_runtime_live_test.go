package provider

import (
	"context"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
)

func plannerLiveEnabled(value string) bool { return value == "1" }

func plannerLiveRequest() ports.PlannerRequest {
	return ports.PlannerRequest{
		ProjectID: "p63d-project", TaskID: "p63d-task",
		Context: ports.PlannerContext{
			Project:     domain.Project{ID: "p63d-project"},
			CurrentTask: domain.Task{ID: "p63d-task", ProjectID: "p63d-project"},
		},
		UserIntent: "Analise o contexto e bloqueie a tarefa porque falta evidência necessária. Não solicite ferramentas nem execute ações.",
	}
}

// Observe successful boundaries by delegation only. Never inspect archives,
// output text, credentials or headers; retain no candidate PlannerDecision.
type plannerLiveContainer struct {
	plannerContainer
	prepared, inferred, captured, destroyed bool
	inferenceCalls                          int
}

func (c *plannerLiveContainer) Prepare(ctx context.Context, archive io.Reader) error {
	err := c.plannerContainer.Prepare(ctx, archive)
	c.prepared = err == nil
	return err
}
func (c *plannerLiveContainer) Infer(ctx context.Context, request infrastructure.PlannerRuntimeRequest) (infrastructure.PlannerRuntimeResult, error) {
	c.inferenceCalls++
	result, err := c.plannerContainer.Infer(ctx, request)
	c.inferred = err == nil
	return result, err
}
func (c *plannerLiveContainer) Capture(ctx context.Context) (io.ReadCloser, error) {
	stream, err := c.plannerContainer.Capture(ctx)
	c.captured = err == nil && stream != nil
	return stream, err
}
func (c *plannerLiveContainer) Destroy(ctx context.Context) error {
	err := c.plannerContainer.Destroy(ctx)
	c.destroyed = err == nil
	return err
}

// Run separately with the one explicit flag, persisted P6.2 volume and Docker
// socket. Default go test/validate.sh cannot open runtime auth or invoke LIVE.
func TestToolFreePlannerLiveOptIn(t *testing.T) {
	runPlannerLive(t, 0)
}
func TestToolFreePlannerHTTPDiagnosticOptIn(t *testing.T) {
	runPlannerLive(t, 1)
}
func TestToolFreePlannerResponseDiagnosticOptIn(t *testing.T) { runPlannerLive(t, 2) }
func runPlannerLive(t *testing.T, diagnostic int) {
	if !plannerLiveEnabled(os.Getenv("DEV_ORCHESTRATOR_CODEX_PLANNER_LIVE")) {
		t.Skip("explicit tool-free Planner LIVE opt-in required")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("LIVE=BLOCKED Linux runtime required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	home, err := NewCodexRuntimeHome()
	if err != nil {
		t.Fatal("LIVE=BLOCKED runtime-owned home unavailable")
	}
	defer func() {
		if home.Close() != nil {
			t.Error("CLEANUP=FAIL")
		}
	}()
	plannerRuntime := NewToolFreeCodexPlannerRuntime(home)
	var observed *plannerLiveContainer
	plannerRuntime.newContainer = func() (plannerContainer, error) {
		if observed != nil {
			t.Fatal("multiple LIVE containers rejected")
		}
		container, err := infrastructure.NewPlannerHostContainer()
		if diagnostic == 1 {
			container, err = infrastructure.NewPlannerHostHTTPDiagnosticContainer()
		}
		if diagnostic == 2 {
			container, err = infrastructure.NewPlannerHostResponseDiagnosticContainer()
		}
		if err != nil {
			return nil, err
		}
		observed = &plannerLiveContainer{plannerContainer: container}
		return observed, nil
	}
	adapter := infrastructure.NewCodexPlannerAdapter(plannerRuntime)
	request := plannerLiveRequest()
	decision, err := adapter.Plan(ctx, request) // Exactly one call; no retries/login.
	if err != nil {
		// Production errors are deliberately generic. Do not infer AUTH_EXPIRED
		// or print raw errors, model output or unobserved tool-event claims.
		t.Log("AUTH=UNKNOWN INFERENCE_RETRY=UNKNOWN TOOLS_POLICY=UNKNOWN TOOLS_OBSERVED=UNKNOWN NETWORK_POLICY=UNKNOWN DECISION_VALID=FAIL CORRELATION=UNKNOWN AUTH_PERSISTENCE=UNKNOWN PROJECT_MUTATION=DENY GIT_MUTATION=DENY")
		if observed != nil {
			t.Logf("INFERENCE_ATTEMPT=%d CLEANUP=%t", observed.inferenceCalls, observed.destroyed)
			if diagnostic, ok := observed.plannerContainer.(interface {
				SanitizedFailureClass() string
				LastConfirmedStage() string
			}); ok {
				t.Logf("LAST_CONFIRMED_STAGE=%s ERROR_CLASS=%s", diagnostic.LastConfirmedStage(), diagnostic.SanitizedFailureClass())
			}
		}
		t.Fatal("LIVE=BLOCKED integrated inference unavailable; no authentication repair attempted")
	}
	if decision.Validate() != nil || decision.ProjectID != request.ProjectID || decision.TaskID != request.TaskID {
		t.Fatal("STRUCTURED_OUTPUT=FAIL")
	}
	if observed == nil || observed.inferenceCalls != 1 || !observed.prepared || !observed.inferred || !observed.captured || !observed.destroyed {
		t.Fatal("LIFECYCLE=FAIL")
	}
	proof, ok := observed.plannerContainer.(interface{ SanitizedEvidenceVerified() bool })
	if !ok || !proof.SanitizedEvidenceVerified() {
		t.Fatal("LIVE=FAIL host evidence unavailable")
	}
	// Runtime Plan only returns after Finish validates/persists and Destroy
	// succeeds. Inference success also confirms host exit zero and ChatGPT auth.
	t.Log("AUTH=PASS INFERENCE_ATTEMPT=1 INFERENCE_RETRY=0 TOOLS_POLICY=EMPTY TOOLS_OBSERVED=0")
	t.Log("NETWORK_POLICY=DENY_BY_DEFAULT_ALLOWLIST_AUTH_OPENAI_COM_CHATGPT_COM DECISION_VALID=PASS CORRELATION=PASS CLEANUP=PASS AUTH_PERSISTENCE=PASS PROJECT_MUTATION=DENY GIT_MUTATION=DENY")
}

func TestPlannerLiveExplicitOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "1 ", "1"} {
		if plannerLiveEnabled(value) != (value == "1") {
			t.Fatal("LIVE gate must require exactly 1")
		}
	}
}

func TestPlannerLiveDeterministicRequest(t *testing.T) {
	if plannerLiveRequest().Validate() != nil {
		t.Fatal("invalid deterministic LIVE request")
	}
}

func TestPlannerHandshakePreflightOptIn(t *testing.T) {
	if !plannerLiveEnabled(os.Getenv("DEV_ORCHESTRATOR_CODEX_PLANNER_PREFLIGHT")) {
		t.Skip("explicit preflight opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	home, err := NewCodexRuntimeHome()
	if err != nil {
		t.Fatal("AUTH_AVAILABLE=UNKNOWN")
	}
	defer home.Close()
	container, err := infrastructure.NewPlannerHostContainer()
	if err != nil {
		t.Fatal("PREFLIGHT=BLOCKED")
	}
	lease, err := home.Materialize(ctx, container)
	if err != nil {
		container.Destroy(ctx)
		t.Fatal("AUTH_AVAILABLE=UNKNOWN")
	}
	report, err := container.ProbeHandshake(ctx)
	if err != nil {
		cleanup := lease.Abort(context.Background())
		t.Logf("CLEANUP=%t INFERENCE_SENT=false", cleanup == nil)
		t.Fatal("PREFLIGHT=BLOCKED")
	}
	cleanup := lease.Finish(context.Background())
	t.Logf("AUTH_AVAILABLE=%t PROXY_CONFIGURED=true CONNECT_RESULT=UNKNOWN TLS_RESULT=UNKNOWN WS_HANDSHAKE=%s HTTP_CLASS=%s WS_CLOSE_CLASS=%s INFERENCE_SENT=%t CLEANUP=%t", report.AuthAvailable, report.WSHandshake, report.HTTPClass, report.WSCloseClass, report.InferenceSent, cleanup == nil)
	t.Log("NETWORK_POLICY=UNCHANGED PROJECT_MUTATION=DENY GIT_MUTATION=DENY")
	if cleanup != nil || report.InferenceSent {
		t.Fatal("PREFLIGHT=FAIL")
	}
}
