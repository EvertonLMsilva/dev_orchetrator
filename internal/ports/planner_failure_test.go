package ports

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlannerFailureClosedAndSanitized(t *testing.T) {
	raw := errors.New("SECRET_PROVIDER")
	f := NewPlannerFailure("SECRET_STAGE", errors.Join(raw, context.Canceled))
	if f.FailureStage() != "PLANNER" || strings.Contains(f.Error(), "SECRET") || errors.Is(f, raw) || !errors.Is(f, context.Canceled) {
		t.Fatal("unsafe planner failure", f)
	}
}
