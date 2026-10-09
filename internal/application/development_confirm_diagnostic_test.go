package application

import (
	"bytes"
	"context"
	"dev-orchestrator/internal/ports"
	"log"
	"strings"
	"testing"
	"time"
)

func TestConfirmExpiredDiagnostic(t *testing.T) {
	var logs bytes.Buffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() { log.SetOutput(writer); log.SetFlags(flags) }()
	now := time.Now()
	identity := strings.Repeat("a", 64)
	cycle := &DevelopmentCycle{pending: &DevelopmentReview{Identity: identity, ExpiresAt: now.Add(-time.Second)}, now: func() time.Time { return now }}
	if _, err := cycle.Confirm(context.Background(), ports.ActorEvidence{}, identity); err == nil {
		t.Fatal("expired accepted")
	}
	if logs.String() != "CONFIRM_EXPIRED\n" {
		t.Fatal("expiration boundary unclassified")
	}
}
