package localsecurity

import (
	"context"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditDurableAppendAndFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	audit := NewAudit(path)
	event := ports.ReadAuditEvent{AttemptID: strings.Repeat("a", 32), Operation: "project.status", ProjectID: "p", CorrelationID: "c", Timestamp: time.Now().UTC(), Outcome: "RECEIVED"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := audit.Append(context.Background(), event); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 8 {
		t.Fatal("lost/interleaved events")
	}
	for _, line := range lines {
		var got ports.ReadAuditEvent
		if json.Unmarshal([]byte(line), &got) != nil || got != event {
			t.Fatal("invalid record")
		}
	}
	event.ErrorClass = "raw error"
	if audit.Append(context.Background(), event) == nil {
		t.Fatal("accepted unsafe event")
	}
	event.ErrorClass = ""
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if audit.Append(ctx, event) == nil {
		t.Fatal("ignored cancellation")
	}
	if NewAudit(filepath.Dir(path)).Append(context.Background(), event) == nil {
		t.Fatal("directory accepted")
	}
	if NewAudit(filepath.Join(t.TempDir(), "missing", "audit")).Append(context.Background(), event) == nil {
		t.Fatal("missing parent accepted")
	}
	// Partial prior append must never be silently joined to a later record.
	put(t, path, `{"partial":`)
	if audit.Append(context.Background(), event) == nil {
		t.Fatal("partial audit record accepted")
	}
	if err := os.Truncate(path, maxAuditFileBytes); err != nil {
		t.Fatal(err)
	}
	if audit.Append(context.Background(), event) == nil {
		t.Fatal("audit size limit bypass")
	}
	link := filepath.Join(t.TempDir(), "audit-link")
	if err := os.Symlink(path, link); err == nil {
		if NewAudit(link).Append(context.Background(), event) == nil {
			t.Fatal("audit symlink accepted")
		}
	}
}
