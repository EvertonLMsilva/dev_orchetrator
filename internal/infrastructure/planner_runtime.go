package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	plannerInputLimit       = 64 * 1024
	plannerOutputLimit      = 16 * 1024
	plannerInstructionLimit = 8 * 1024
	plannerSchemaLimit      = 16 * 1024
	plannerTimeout          = 30 * time.Second
)

var ErrInvalidPlannerRuntimeRequest = errors.New("invalid planner runtime request")

// PlannerRuntime owns inference only. A concrete Codex host MUST capture
// ToolPolicy.allowed_tools = Some(vec![]) before thread startup. This invariant
// is mandatory and cannot be changed by caller input, instructions or schema.
// The runtime returns only a completed final structured message after successful
// turn completion and resource/auth cleanup. No tool execution is authorized.
type PlannerRuntime interface {
	Plan(context.Context, PlannerRuntimeRequest) (PlannerRuntimeResult, error)
}

// PlannerRuntimeRequest supplies declarative data, never executable authority.
// Instructions and Input are untrusted data; OutputSchema constrains output only.
// The runtime owns credentials, paths, tools, plugins, network and process policy.
type PlannerRuntimeRequest struct {
	Instructions string
	Input        json.RawMessage
	OutputSchema json.RawMessage
	Limits       PlannerRuntimeLimits
}

type PlannerRuntimeLimits struct {
	MaxOutputBytes int
	Timeout        time.Duration
}

func (r PlannerRuntimeRequest) Validate() error {
	if strings.TrimSpace(r.Instructions) == "" || len(r.Instructions) > plannerInstructionLimit ||
		!utf8.ValidString(r.Instructions) || len(r.Input) == 0 || len(r.Input) > plannerInputLimit ||
		!utf8.Valid(r.Input) || !json.Valid(r.Input) || len(r.OutputSchema) == 0 ||
		len(r.OutputSchema) > plannerSchemaLimit || !utf8.Valid(r.OutputSchema) || !json.Valid(r.OutputSchema) ||
		r.Limits.MaxOutputBytes <= 0 || r.Limits.MaxOutputBytes > plannerOutputLimit ||
		r.Limits.Timeout <= 0 || r.Limits.Timeout > plannerTimeout {
		return ErrInvalidPlannerRuntimeRequest
	}
	return nil
}

type PlannerRuntimeResult struct {
	StructuredOutput []byte
}
