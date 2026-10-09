//go:build linux

package composition

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperationalConfirmDiagnosticsAndRetry(t *testing.T) {
	for _, mode := range []string{"valid", "lookup", "approval-denied"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			writer, flags := log.Writer(), log.Flags()
			log.SetOutput(&logs)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(writer); log.SetFlags(flags) })
			c := operationalFixture(t)
			if mode == "approval-denied" {
				c.Grants = c.Grants[3:]
			}
			s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
			ctx := context.Background()
			if s.Handle(ctx, operationalInput(c, 0, "begin", "create note.txt")).Status != "REVIEW_REQUIRED" {
				t.Fatal("begin")
			}
			r := operationalRecord(t, s, "a")
			in := operationalInput(c, 0, "confirm", "")
			in.Confirmation = r.Output.Review.Identity
			if mode == "lookup" {
				in.Confirmation = strings.Repeat("f", 64)
			}
			out := s.Handle(ctx, in)
			expected := map[string]string{"valid": "CONFIRM_APPROVED", "lookup": "CONFIRM_LOOKUP_DENIED", "approval-denied": "CONFIRM_APPROVAL_DENIED"}[mode]
			if !strings.Contains(logs.String(), expected+"\n") {
				t.Fatal("boundary unclassified", expected)
			}
			if strings.Contains(logs.String(), in.Confirmation) {
				t.Fatal("identity leaked")
			}
			if mode == "valid" {
				if out.Status != "REVIEW_REQUIRED" || s.records[r.CorrelationID].Output.Review.Operation != "GIT_BRANCH" {
					t.Fatal("approval flow changed")
				}
				path := filepath.Join(c.Registry.Projects[0].Workspace, r.CorrelationID, "state.json")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if s.Handle(ctx, in).Status != "REJECTED" {
					t.Fatal("old identity reused")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("retry duplicated approval/write/git")
				}
			} else if out.Status != "REJECTED" || s.records[r.CorrelationID].Output.Result.WriteTransaction != "" {
				t.Fatal("denial applied WRITE")
			}
		})
	}
}
