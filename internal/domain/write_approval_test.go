package domain

import (
	"testing"
	"time"
)

func writeApproval() (WriteApproval, WriteCandidate, WriteBinding, time.Time) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	c := writeCandidate()
	b, err := c.Binding("policy-1", "nonce-1")
	if err != nil {
		panic(err)
	}
	return WriteApproval{SchemaVersion: 1, ApprovalID: "approval", WriteBinding: b, ApproverIdentity: "authenticated-actor", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}, c, b, now
}
func TestWriteApprovalExactBindings(t *testing.T) {
	a, c, b, now := writeApproval()
	if err := ValidateWriteAuthorization(a, c, b, now); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*WriteBinding){
		"project": func(b *WriteBinding) { b.ProjectID = "other" }, "task": func(b *WriteBinding) { b.TaskID = "other" },
		"correlation": func(b *WriteBinding) { b.CorrelationID = "other" }, "workspace": func(b *WriteBinding) { b.WorkspaceIdentity = "other" },
		"base": func(b *WriteBinding) { b.BaseIdentity = writeHash("other") }, "operation": func(b *WriteBinding) { b.OperationKind = "GIT_COMMIT" },
		"targets": func(b *WriteBinding) { b.AllowedTargets = []string{"src/a.go"} }, "diff": func(b *WriteBinding) { b.ApprovedDiffIdentity = writeHash("other") },
		"artifact": func(b *WriteBinding) { b.ApprovedArtifactID = "other" }, "post": func(b *WriteBinding) { b.ExpectedPostIdentity = writeHash("other") },
		"parameters": func(b *WriteBinding) { b.OperationParametersIdentity = writeHash("other") }, "policy": func(b *WriteBinding) { b.PolicyVersion = "other" },
		"nonce": func(b *WriteBinding) { b.Nonce = "other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := b.Clone()
			mutate(&changed)
			if ValidateWriteAuthorization(a, c, changed, now) == nil {
				t.Fatal("mismatch allowed")
			}
			changedApproval := a.Clone()
			mutate(&changedApproval.WriteBinding)
			if ValidateWriteAuthorization(changedApproval, c, b, now) == nil {
				t.Fatal("changed approval allowed")
			}
		})
	}
	if ValidateWriteAuthorization(a, c, b, a.ExpiresAt) == nil {
		t.Fatal("expiry boundary allowed")
	}
	if ValidateWriteAuthorization(a, c, b, a.IssuedAt.Add(-time.Nanosecond)) == nil {
		t.Fatal("future issue allowed")
	}
	c.Manifest.Entries[0].Postimage = writeHash("tampered")
	if ValidateWriteAuthorization(a, c, b, now) == nil {
		t.Fatal("candidate tamper allowed")
	}
}

func TestWriteApprovalTargetOrderAndAliases(t *testing.T) {
	a, c, b, now := writeApproval()
	a.AllowedTargets[0], a.AllowedTargets[1] = a.AllowedTargets[1], a.AllowedTargets[0]
	if ValidateWriteAuthorization(a, c, b, now) != nil {
		t.Fatal("set order changed authority")
	}
	a.AllowedTargets = append(a.AllowedTargets, a.AllowedTargets[0])
	if ValidateWriteAuthorization(a, c, b, now) == nil {
		t.Fatal("duplicate target allowed")
	}
}
func TestWriteApprovalRequiredFields(t *testing.T) {
	cases := map[string]func(*WriteApproval){
		"version": func(a *WriteApproval) { a.SchemaVersion = 0 }, "id": func(a *WriteApproval) { a.ApprovalID = "" },
		"actor": func(a *WriteApproval) { a.ApproverIdentity = "" }, "issue": func(a *WriteApproval) { a.IssuedAt = time.Time{} },
		"expiry": func(a *WriteApproval) { a.ExpiresAt = a.IssuedAt }, "nonce": func(a *WriteApproval) { a.Nonce = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			a, _, _, _ := writeApproval()
			mutate(&a)
			if a.Validate() == nil {
				t.Fatal("invalid approval allowed")
			}
		})
	}
}

func TestWriteConsumptionTerminalStates(t *testing.T) {
	a, _, _, now := writeApproval()
	for _, state := range []WriteConsumptionState{WriteApplied, WriteAborted, WriteRecoveryRequired} {
		t.Run(string(state), func(t *testing.T) {
			r := WriteApprovalRecord{Approval: a, State: WriteAvailable}
			reserved, granted, err := r.Reserve("tx", now)
			if err != nil || !granted {
				t.Fatal(reserved, granted, err)
			}
			result := WriteTerminalResult{State: state, ResultIdentity: a.ExpectedPostIdentity}
			finished, err := reserved.Finish("tx", result)
			if err != nil || finished.Validate() != nil {
				t.Fatal(finished, err)
			}
			replay, granted, err := finished.Reserve("second", now)
			if err != nil || granted || replay.TransactionID != "tx" || replay.Result != result {
				t.Fatal(replay, granted, err)
			}
			if _, err := finished.Finish("wrong", result); err == nil {
				t.Fatal("foreign transaction replay accepted")
			}
			if state == WriteApplied {
				finished.Result.ResultIdentity = writeHash("wrong post state")
				if finished.Validate() == nil {
					t.Fatal("APPLIED record without expected post identity")
				}
			}
		})
	}
	for _, state := range []WriteConsumptionState{"", "unknown", WriteAvailable, WriteReserved} {
		if (WriteTerminalResult{State: state, ResultIdentity: a.ExpectedPostIdentity}).Validate() == nil {
			t.Fatal("invalid terminal", state)
		}
	}
}
