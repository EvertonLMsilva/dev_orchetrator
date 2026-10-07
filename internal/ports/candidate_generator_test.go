package ports_test

import (
	"context"
	"dev-orchestrator/internal/ports"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type toolFreeCandidateDouble struct{}

func (toolFreeCandidateDouble) ProveToolFree(context.Context) error {
	return errors.New("no concrete provider proof")
}
func (toolFreeCandidateDouble) Generate(context.Context, ports.CandidateGenerationRequest) ([]byte, error) {
	return nil, errors.New("never invoked without proof")
}

var _ ports.CandidateGenerator = toolFreeCandidateDouble{}

func TestCandidatePortHasNoRuntimeAuthority(t *testing.T) {
	r := reflect.TypeOf(ports.CandidateGenerationRequest{})
	for i := 0; i < r.NumField(); i++ {
		name := strings.ToLower(r.Field(i).Name)
		for _, forbidden := range []string{"root", "workspace", "shell", "tool", "command", "approval", "path"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("authority field %s", name)
			}
		}
	}
	if (toolFreeCandidateDouble{}).ProveToolFree(context.Background()) == nil {
		t.Fatal("unproven config accepted")
	}
}
