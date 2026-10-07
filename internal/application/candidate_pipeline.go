package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"fmt"
	"time"
)

var ErrCandidateCleanup = ports.ErrCandidateCleanup

type CandidateRequest struct {
	Context           domain.CandidateContext
	Objective         string
	SourceRoot        string
	WorkspaceIdentity string
	Policy            domain.CandidatePolicy
	Timeout           time.Duration
}
type CandidatePipeline struct {
	generator ports.CandidateGenerator
	factory   ports.CandidateWorkspaceFactory
}

func NewCandidatePipeline(g ports.CandidateGenerator, f ports.CandidateWorkspaceFactory) *CandidatePipeline {
	return &CandidatePipeline{generator: g, factory: f}
}

// No WorkflowEngine transition, approval, real applier, or Git operation occurs.
func (p *CandidatePipeline) Generate(ctx context.Context, r CandidateRequest) (artifact domain.CandidateArtifact, err error) {
	if p == nil || p.generator == nil || p.factory == nil || r.Context.Validate() != nil || r.Policy.Validate() != nil || r.Timeout <= 0 || r.Timeout > 10*time.Minute || r.Objective == "" || len(r.Objective) > 16384 {
		return artifact, domain.ErrCandidateDenied
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	r.Policy = r.Policy.Clone()
	if err = p.generator.ProveToolFree(ctx); err != nil {
		return artifact, err
	}
	if err = ctx.Err(); err != nil {
		return artifact, err
	}
	w, prepareErr := p.factory.Prepare(ctx, r.SourceRoot, r.WorkspaceIdentity, r.Policy)
	if w != nil {
		defer func() {
			if closeErr := w.Close(); closeErr != nil {
				artifact = domain.CandidateArtifact{}
				err = fmt.Errorf("%w: %v", ErrCandidateCleanup, closeErr)
			}
		}()
	}
	if prepareErr != nil {
		return artifact, prepareErr
	}
	if w == nil {
		return artifact, domain.ErrCandidateDenied
	}
	inputs := append([]domain.CandidateFile(nil), w.Inputs()...)
	for i := range inputs {
		inputs[i] = inputs[i].Clone()
	}
	output, err := p.generator.Generate(ctx, ports.CandidateGenerationRequest{Context: r.Context, Objective: r.Objective, Inputs: inputs, WriteTargets: append([]string(nil), r.Policy.WriteTargets...), SchemaVersion: domain.WriteSchemaVersion})
	if err != nil {
		return artifact, err
	}
	if err = ctx.Err(); err != nil {
		return artifact, err
	}
	if int64(len(output)) > r.Policy.Limits.MaxOutputBytes {
		return artifact, domain.ErrCandidateDenied
	}
	if err = w.Write(ctx, append([]byte(nil), output...)); err != nil {
		return artifact, err
	}
	if err = ctx.Err(); err != nil {
		return artifact, err
	}
	artifact, err = w.Extract(ctx, r.Context)
	if err != nil {
		return domain.CandidateArtifact{}, err
	}
	if err = ctx.Err(); err != nil {
		return domain.CandidateArtifact{}, err
	}
	return artifact, nil
}
