package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidExecutionLimits = errors.New("invalid execution limits")
	ErrExecutionOutputLimit   = errors.New("execution output limit exceeded")
	ErrInvalidExecutionOutput = errors.New("execution output must be valid UTF-8")
)

// ExecutionLimits is an immutable application-controlled policy for one attempt.
// Its zero value is invalid; both limits must be strictly positive.
type ExecutionLimits struct {
	timeout        time.Duration
	maxOutputBytes int
}

func NewExecutionLimits(timeout time.Duration, maxOutputBytes int) (ExecutionLimits, error) {
	l := ExecutionLimits{timeout: timeout, maxOutputBytes: maxOutputBytes}
	if err := l.Validate(); err != nil {
		return ExecutionLimits{}, err
	}
	return l, nil
}

func (l ExecutionLimits) Timeout() time.Duration { return l.timeout }
func (l ExecutionLimits) MaxOutputBytes() int    { return l.maxOutputBytes }
func (l ExecutionLimits) Validate() error {
	if l.timeout <= 0 || l.maxOutputBytes <= 0 {
		return ErrInvalidExecutionLimits
	}
	return nil
}

// BoundedExecutionContext preserves parent cancellation and earlier deadlines.
// The caller must defer cancel immediately, including on normal completion.
func BoundedExecutionContext(parent context.Context, limits ExecutionLimits) (context.Context, context.CancelFunc, error) {
	if err := limits.Validate(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, limits.Timeout())
	return ctx, cancel, nil
}

// ExecutionOutput aggregates complete UTF-8 text chunks across an attempt.
// The caller serializes access. Rejection is sticky so it cannot become success
// by ignoring a failed chunk and appending a smaller one later.
type ExecutionOutput struct {
	text  strings.Builder
	limit int
	err   error
}

func NewExecutionOutput(limits ExecutionLimits) (*ExecutionOutput, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &ExecutionOutput{limit: limits.MaxOutputBytes()}, nil
}

func (o *ExecutionOutput) Append(chunk string) error {
	if o == nil || o.limit <= 0 {
		return ErrInvalidExecutionLimits
	}
	if o.err != nil {
		return o.err
	}
	if len(chunk) > o.limit-o.text.Len() {
		o.err = ErrExecutionOutputLimit
		return o.err
	}
	if !utf8.ValidString(chunk) {
		o.err = ErrInvalidExecutionOutput
		return o.err
	}
	o.text.WriteString(chunk)
	return nil
}

func (o *ExecutionOutput) String() string { return o.text.String() }
