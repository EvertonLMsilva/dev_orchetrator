package localsecurity

import (
	"context"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Audit appends JSONL and Syncs before acknowledging. Use one writer instance per
// file. Directory ownership and filesystem durability are operator prerequisites.
type Audit struct {
	path string
	mu   sync.Mutex
}

func NewAudit(path string) *Audit { return &Audit{path: path} }

const maxAuditFileBytes = 16 * 1024 * 1024

func (a *Audit) Append(ctx context.Context, event ports.ReadAuditEvent) error {
	if a == nil || ctx.Err() != nil || !trustedPath(a.path) || !validAudit(event) {
		return ErrSecurity
	}
	data, err := json.Marshal(event)
	if err != nil {
		return ErrSecurity
	}
	data = append(data, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		return ErrSecurity
	}
	// Reject nonregular files/symlinks and verify opened identity. Administrative
	// paths must be outside projects and protected from untrusted local writers.
	info, statErr := os.Lstat(a.path)
	var f *os.File
	if os.IsNotExist(statErr) {
		f, err = os.OpenFile(a.path, os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0600)
	} else {
		if statErr != nil || !info.Mode().IsRegular() {
			return ErrSecurity
		}
		f, err = os.OpenFile(a.path, os.O_RDWR|os.O_APPEND, 0600)
	}
	if err != nil {
		return ErrSecurity
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || (info != nil && !os.SameFile(info, opened)) {
		f.Close()
		return ErrSecurity
	}
	if opened.Size()+int64(len(data)) > maxAuditFileBytes {
		f.Close()
		return ErrSecurity
	}
	if opened.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], opened.Size()-1); err != nil || last[0] != '\n' {
			f.Close()
			return ErrSecurity
		}
	}
	n, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || n != len(data) || syncErr != nil || closeErr != nil || ctx.Err() != nil {
		return ErrSecurity
	}
	if info == nil {
		// Persist the directory entry as well as the newly created file content.
		dir, err := os.Open(filepath.Dir(a.path))
		if err != nil {
			return ErrSecurity
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil || closeErr != nil {
			return ErrSecurity
		}
	}
	return nil
}

func validAudit(e ports.ReadAuditEvent) bool {
	if len(e.AttemptID) != 32 || !validID(e.AttemptID) || e.Timestamp.IsZero() || (e.PrincipalID != "" && !validID(e.PrincipalID)) || (e.ProjectID != "" && !validID(e.ProjectID)) || (e.CorrelationID != "" && !validID(e.CorrelationID)) || (!validOperation(e.Operation) && e.Operation != "UNKNOWN") {
		return false
	}
	switch e.Outcome {
	case "RECEIVED":
		return e.PrincipalID == "" && e.ErrorClass == ""
	case "AUTHORIZED", "SUCCESS":
		return e.PrincipalID != "" && validOperation(e.Operation) && e.ProjectID != "" && e.CorrelationID != "" && e.ErrorClass == ""
	case "AUTHENTICATION_DENIED":
		return e.ErrorClass == "ACCESS_DENIED"
	case "AUTHORIZATION_DENIED":
		return e.ErrorClass == "ACCESS_DENIED"
	case "READ_FAILURE":
		return e.PrincipalID != "" && e.ErrorClass == "READ_UNAVAILABLE"
	}
	return false
}
