package composition

import (
	"bytes"
	"dev-orchestrator/internal/domain"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

var ErrStorage = errors.New("operational storage unavailable")

const maxAuditBytes = 1024 * 1024

type auditRecord struct {
	Correlation    string
	ProjectID      domain.ProjectID
	GuildID        string
	ChannelID      string
	TaskID         domain.TaskID
	Stage          string
	Status         string
	PlannerCalls   int
	EvidenceCalls  int
	EvidenceKind   domain.ActionType
	EvidenceStatus string
	Decision       string
	CreatedAt      time.Time
}
type auditStore struct {
	mu      sync.Mutex
	root    *os.Root
	records []auditRecord
}

func openAudit(dir string) (*auditStore, error) {
	root, e := os.OpenRoot(dir)
	if e != nil {
		return nil, ErrStorage
	}
	a := &auditStore{root: root, records: []auditRecord{}}
	// Exclusive instance lease is retained after an unclean stop. Recovery is
	// explicit and must first prove there are no active operations.
	if e = root.Mkdir("instance.lock", 0700); e != nil {
		root.Close()
		return nil, ErrStorage
	}
	fail := func() (*auditStore, error) { root.Remove("instance.lock"); root.Close(); return nil, ErrStorage }
	if _, e := root.Lstat("audit.pending"); !errors.Is(e, os.ErrNotExist) {
		return fail()
	}
	info, e := root.Lstat("audit.json")
	if errors.Is(e, os.ErrNotExist) {
		return a, nil
	}
	if e != nil || !info.Mode().IsRegular() || info.Size() > maxAuditBytes {
		return fail()
	}
	f, e := root.Open("audit.json")
	if e != nil {
		return fail()
	}
	data, e := io.ReadAll(io.LimitReader(f, maxAuditBytes+1))
	f.Close()
	if e != nil || len(data) > maxAuditBytes || uniqueJSON(data) != nil {
		return fail()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&a.records) != nil || a.records == nil {
		return fail()
	}
	for _, r := range a.records {
		if !validAudit(r) {
			return fail()
		}
	}
	if !completeAudit(a.records) {
		return fail()
	}
	return a, nil
}
func validAudit(r auditRecord) bool {
	if !validDiscordID(r.GuildID) || !validDiscordID(r.ChannelID) {
		return false
	}
	if len(r.Correlation) != 32 || r.ProjectID == "" || r.CreatedAt.IsZero() || r.PlannerCalls < 0 || r.PlannerCalls > 2 || r.EvidenceCalls < 0 || r.EvidenceCalls > 1 {
		return false
	}
	for _, c := range r.Correlation {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	if r.Stage != "START" && r.Stage != "RUN" && r.Stage != "FINISH" {
		return false
	}
	switch r.Status {
	case "PENDING", "BLOCKED", "LIMITED", "REJECTED", "BUSY":
	default:
		return false
	}
	if r.EvidenceKind != "" && !readOnlyKind(r.EvidenceKind) {
		return false
	}
	switch r.EvidenceStatus {
	case "", "SUCCESS", "FAILED", "BLOCKED", "APPROVAL_REQUIRED":
	default:
		return false
	}
	switch r.Decision {
	case "", "BLOCK", "REQUEST_EVIDENCE", "DENIED":
	default:
		return false
	}
	return true
}

func completeAudit(records []auditRecord) bool {
	pending := map[string]auditRecord{}
	seen := map[string]bool{}
	for _, r := range records {
		if r.Stage == "START" {
			if seen[r.Correlation] || r.Status != "PENDING" || r.TaskID != "" || r.PlannerCalls != 0 || r.EvidenceCalls != 0 {
				return false
			}
			seen[r.Correlation] = true
			pending[r.Correlation] = r
			continue
		}
		start, ok := pending[r.Correlation]
		if !ok || start.ProjectID != r.ProjectID || start.GuildID != r.GuildID || start.ChannelID != r.ChannelID || r.CreatedAt.Before(start.CreatedAt) {
			return false
		}
		if r.Stage == "RUN" {
			if start.Stage != "START" || r.Status != "PENDING" || r.TaskID == "" {
				return false
			}
			pending[r.Correlation] = r
			continue
		}
		if r.Status == "PENDING" || (start.TaskID != "" && start.TaskID != r.TaskID) {
			return false
		}
		delete(pending, r.Correlation)
	}
	return len(pending) == 0
}
func readOnlyKind(k domain.ActionType) bool {
	switch k {
	case domain.ActionTypeReadFile, domain.ActionTypeSearch, domain.ActionTypeGitDiff, domain.ActionTypeGitStatus:
		return true
	}
	return false
}
func (a *auditStore) append(r auditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validAudit(r) || a.root == nil {
		return ErrStorage
	}
	next := append(append([]auditRecord{}, a.records...), r)
	data, e := json.Marshal(next)
	if e != nil || len(data) > maxAuditBytes {
		return ErrStorage
	}
	f, e := a.root.OpenFile("audit.pending", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrStorage
	}
	defer a.root.Remove("audit.pending")
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return ErrStorage
	}
	if a.root.Rename("audit.pending", "audit.json") != nil {
		return ErrStorage
	}
	dir, e := a.root.Open(".")
	if e != nil {
		return ErrStorage
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return ErrStorage
	}
	a.records = next
	return nil
}
func (a *auditStore) close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root == nil {
		return nil
	}
	e := a.root.Remove("instance.lock")
	closeErr := a.root.Close()
	a.root = nil
	if e != nil || closeErr != nil {
		return ErrStorage
	}
	return nil
}
