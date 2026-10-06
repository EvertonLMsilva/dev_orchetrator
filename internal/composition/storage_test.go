package composition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditRejectsIncompleteDuplicateAndImpossibleRecords(t *testing.T) {
	start := auditRecord{Correlation: "0123456789abcdef0123456789abcdef", ProjectID: "p", GuildID: "1", ChannelID: "2", Stage: "START", Status: "PENDING", CreatedAt: time.Now().UTC()}
	for _, records := range [][]auditRecord{
		{start},
		{start, start},
		{{Correlation: start.Correlation, ProjectID: "p", GuildID: "1", ChannelID: "2", Stage: "FINISH", Status: "BLOCKED", CreatedAt: start.CreatedAt}},
	} {
		dir := t.TempDir()
		data, _ := json.Marshal(records)
		if e := os.WriteFile(filepath.Join(dir, "audit.json"), data, 0600); e != nil {
			t.Fatal(e)
		}
		if a, e := openAudit(dir); e == nil {
			a.close()
			t.Fatal("inconsistent audit accepted")
		}
	}
}
func TestAuditCapacityFailsClosed(t *testing.T) {
	dir := t.TempDir()
	a, e := openAudit(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer a.close()
	record := auditRecord{Correlation: "0123456789abcdef0123456789abcdef", ProjectID: "p", GuildID: "1", ChannelID: "2", Stage: "START", Status: "PENDING", CreatedAt: time.Now().UTC()}
	a.records = make([]auditRecord, 10000)
	for i := range a.records {
		a.records[i] = record
	}
	if a.append(record) == nil {
		t.Fatal("unbounded audit accepted")
	}
	if _, e := os.Stat(filepath.Join(dir, "audit.json")); !os.IsNotExist(e) {
		t.Fatal("oversized snapshot published")
	}
}
