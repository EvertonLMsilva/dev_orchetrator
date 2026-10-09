package application_test

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type candidateGeneratorDouble struct {
	proveErr error
	calls    int
	generate func(context.Context, ports.CandidateGenerationRequest) ([]byte, error)
}

func (g *candidateGeneratorDouble) ProveToolFree(context.Context) error { return g.proveErr }
func (g *candidateGeneratorDouble) Generate(c context.Context, r ports.CandidateGenerationRequest) ([]byte, error) {
	g.calls++
	return g.generate(c, r)
}

type candidateFactoryDouble struct {
	w     *candidateWorkspaceDouble
	calls int
	err   error
}

func (f *candidateFactoryDouble) Prepare(context.Context, string, string, domain.CandidatePolicy) (ports.CandidateWorkspace, error) {
	f.calls++
	return f.w, f.err
}

type candidateWorkspaceDouble struct {
	writes     int
	extracts   int
	closed     int
	writeErr   error
	extractErr error
	closeErr   error
	artifact   domain.CandidateArtifact
	inputs     []domain.CandidateFile
}

func (w *candidateWorkspaceDouble) Inputs() []domain.CandidateFile { return w.inputs }
func (w *candidateWorkspaceDouble) Write(context.Context, []byte) error {
	w.writes++
	return w.writeErr
}
func (w *candidateWorkspaceDouble) Extract(context.Context, domain.CandidateContext) (domain.CandidateArtifact, error) {
	w.extracts++
	return w.artifact, w.extractErr
}
func (w *candidateWorkspaceDouble) Close() error { w.closed++; return w.closeErr }
func pipelineCandidateRequest() application.CandidateRequest {
	return application.CandidateRequest{Context: domain.CandidateContext{ProjectID: "p", TaskID: "t", CorrelationID: "c"}, Objective: "edit", RequestedWriteTargets: []string{"a"}, SourceRoot: "/real", WorkspaceIdentity: "registered", Timeout: time.Second, Policy: domain.CandidatePolicy{InputTargets: []string{"a"}, WriteTargets: []string{"a"}, Limits: domain.CandidateLimits{MaxOperations: 1, MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxPathBytes: 256, MaxOutputBytes: 4096}}}
}
func TestCandidatePipelineToolsAndFailureLifecycle(t *testing.T) {
	injected := errors.New("injected failure")
	for _, stage := range []string{"toolProof", "prepare", "generator", "partialWrite", "extract", "cleanup", "cancel", "timeout", "outputLimit"} {
		t.Run(stage, func(t *testing.T) {
			w := &candidateWorkspaceDouble{inputs: []domain.CandidateFile{{Target: "a", Content: []byte("old")}}, artifact: testPipelineArtifact(t)}
			f := &candidateFactoryDouble{w: w}
			g := &candidateGeneratorDouble{generate: func(_ context.Context, r ports.CandidateGenerationRequest) ([]byte, error) {
				r.Inputs[0].Content[0] = '!'
				return []byte("proposal"), nil
			}}
			r := pipelineCandidateRequest()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "toolProof":
				g.proveErr = injected
			case "prepare":
				f.err = injected
			case "generator":
				g.generate = func(context.Context, ports.CandidateGenerationRequest) ([]byte, error) { return nil, injected }
			case "partialWrite":
				w.writeErr = injected
			case "extract":
				w.extractErr = injected
			case "cleanup":
				w.closeErr = injected
			case "cancel":
				g.generate = func(context.Context, ports.CandidateGenerationRequest) ([]byte, error) { cancel(); return nil, nil }
			case "timeout":
				r.Timeout = 10 * time.Millisecond
				g.generate = func(ctx context.Context, _ ports.CandidateGenerationRequest) ([]byte, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}
			case "outputLimit":
				g.generate = func(context.Context, ports.CandidateGenerationRequest) ([]byte, error) {
					return make([]byte, 4097), nil
				}
			}
			artifact, err := application.NewCandidatePipeline(g, f).Generate(ctx, r)
			if err == nil || artifact.Candidate().Validate() == nil {
				t.Fatal("failure promoted candidate")
			}
			if stage == "toolProof" {
				if f.calls != 0 || g.calls != 0 {
					t.Fatal("tool failure continued")
				}
			} else if w.closed != 1 {
				t.Fatal("copy not closed")
			}
			if string(w.inputs[0].Content) != "old" {
				t.Fatal("generator aliased snapshot")
			}
			if stage == "cleanup" && !errors.Is(err, application.ErrCandidateCleanup) {
				t.Fatal("cleanup not explicit BLOCK")
			}
			if (stage == "cancel" || stage == "timeout" || stage == "outputLimit" || stage == "generator" || stage == "prepare") && w.writes != 0 {
				t.Fatal("failure wrote copy")
			}
		})
	}
}

func testPipelineArtifact(t *testing.T) domain.CandidateArtifact {
	t.Helper()
	digest := domain.CandidateDigest([]byte("new"))
	c := domain.WriteCandidate{SchemaVersion: 1, ArtifactID: "candidate", ProjectID: "p", TaskID: "t", CorrelationID: "c", WorkspaceIdentity: "w", BaseIdentity: digest, ExpectedPostIdentity: digest, OperationParametersIdentity: digest, Manifest: domain.WriteManifest{SchemaVersion: 1, Entries: []domain.WriteManifestEntry{{Target: "a", Operation: domain.WriteCreate, FileType: "regular", Postimage: digest, AfterMode: 0644}}}}
	a, err := domain.NewCandidateArtifact(c, map[string][]byte{digest: []byte("new")})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestCandidatePipelineEndToEndDeterministic(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Linux backend required")
	}
	source := t.TempDir()
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	r := pipelineCandidateRequest()
	r.SourceRoot = source
	proved := false
	g := &candidateGeneratorDouble{generate: func(_ context.Context, request ports.CandidateGenerationRequest) ([]byte, error) {
		proved = true
		if request.Context != r.Context || len(request.Inputs) != 1 || request.Inputs[0].Target != "a" || string(request.Inputs[0].Content) != "old" {
			t.Fatal("bad generator data")
		}
		request.Inputs[0].Content[0] = '!'
		return json.Marshal(domain.StructuredProposal{SchemaVersion: 1, Edits: []domain.StructuredEdit{{Operation: domain.WriteReplace, Target: "a", ExpectedPreimageIdentity: domain.CandidateDigest([]byte("old")), PostimageContent: base64.StdEncoding.EncodeToString([]byte("new")), PostimageIdentity: domain.CandidateDigest([]byte("new"))}}})
	}}
	a, err := application.NewCandidatePipeline(g, infrastructure.NewCandidateWorkspaceFactory(scratch)).Generate(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !proved || a.Candidate().Validate() != nil {
		t.Fatal("candidate missing")
	}
	entries, _ := os.ReadDir(scratch)
	if len(entries) != 0 {
		t.Fatal("copy retained")
	}
	content, _ := os.ReadFile(filepath.Join(source, "a"))
	if string(content) != "old" {
		t.Fatal("source modified")
	}
}

func TestCandidateRealCopyMaliciousAndCancelledGenerators(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Linux backend required")
	}
	for _, kind := range []string{"malicious", "cancel", "timeout", "crash"} {
		t.Run(kind, func(t *testing.T) {
			source := t.TempDir()
			scratch := t.TempDir()
			os.WriteFile(filepath.Join(source, "a"), []byte("old"), 0644)
			r := pipelineCandidateRequest()
			r.SourceRoot = source
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g := &candidateGeneratorDouble{generate: func(ctx context.Context, _ ports.CandidateGenerationRequest) ([]byte, error) {
				switch kind {
				case "cancel":
					cancel()
					return nil, nil
				case "timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				case "crash":
					return nil, errors.New("provider failed")
				default:
					return []byte(`{"SchemaVersion":1,"Edits":[{"Operation":"CREATE","Target":"../real/a","ExpectedPreimageIdentity":"","PostimageContent":"eA==","PostimageIdentity":"bad"}]}`), nil
				}
			}}
			if kind == "timeout" {
				r.Timeout = 10 * time.Millisecond
			}
			artifact, err := application.NewCandidatePipeline(g, infrastructure.NewCandidateWorkspaceFactory(scratch)).Generate(ctx, r)
			if err == nil || artifact.Candidate().Validate() == nil {
				t.Fatal("failure promoted")
			}
			entries, _ := os.ReadDir(scratch)
			if len(entries) != 0 {
				t.Fatal("failed copy retained")
			}
			content, _ := os.ReadFile(filepath.Join(source, "a"))
			if string(content) != "old" {
				t.Fatal("malicious generator changed real bytes")
			}
		})
	}
}

func TestCandidatePipelineStructuredTargetsGate(t *testing.T) {
	for _, scenario := range []string{"outside-policy", "missing", "substitution", "artifact-outside-policy", "allowed", "generator-mutation"} {
		t.Run(scenario, func(t *testing.T) {
			r := pipelineCandidateRequest()
			w := &candidateWorkspaceDouble{artifact: testPipelineArtifact(t)}
			f := &candidateFactoryDouble{w: w}
			g := &candidateGeneratorDouble{generate: func(_ context.Context, q ports.CandidateGenerationRequest) ([]byte, error) {
				if scenario == "generator-mutation" {
					q.WriteTargets[0] = "attacker"
				}
				return []byte("proposal"), nil
			}}
			switch scenario {
			case "outside-policy":
				r.RequestedWriteTargets = []string{"summary.txt"}
			case "missing":
				r.RequestedWriteTargets = nil
			case "substitution":
				r.Policy.WriteTargets = []string{"a", "b"}
				r.RequestedWriteTargets = []string{"b"}
			case "artifact-outside-policy":
				r.Policy.WriteTargets = []string{"b"}
				r.RequestedWriteTargets = []string{"b"}
			}
			a, err := application.NewCandidatePipeline(g, f).Generate(context.Background(), r)
			if scenario == "allowed" || scenario == "generator-mutation" {
				if err != nil || a.Candidate().Validate() != nil {
					t.Fatal(a, err)
				}
			} else {
				if !errors.Is(err, domain.ErrCandidateDenied) || len(a.Candidate().Manifest.Entries) != 0 {
					t.Fatal("artifact escaped", a, err)
				}
			}
			if scenario == "outside-policy" || scenario == "missing" {
				if g.calls != 0 || f.calls != 0 || w.writes != 0 {
					t.Fatal("pre-generation effects")
				}
			} else if g.calls != 1 || w.closed != 1 {
				t.Fatal("generation or cleanup missing")
			}
			if scenario == "generator-mutation" && r.RequestedWriteTargets[0] != "a" {
				t.Fatal("generator changed intent")
			}
		})
	}
}
