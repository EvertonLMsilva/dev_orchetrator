package localsecurity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestLocalAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authentication.json")
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(token)
	valid := fmt.Sprintf(`{"credentials":[{"principalId":"user","sha256":"%s"}]}`, hex.EncodeToString(sum[:]))
	put(t, path, valid)
	a := NewAuthentication(path)
	p, err := a.Authenticate(context.Background(), ports.AuthenticationEvidence{Material: token})
	if err != nil || p.ID != "user" {
		t.Fatal("valid authentication failed")
	}
	for _, evidence := range [][]byte{nil, []byte("user"), []byte("wrong"), make([]byte, 4097)} {
		if p, err := a.Authenticate(context.Background(), ports.AuthenticationEvidence{Material: evidence}); err == nil || p.ID != "" {
			t.Fatal("accepted invalid evidence")
		}
	}
	for _, config := range []string{`{}`, `null`, `{"credentials":[]}`, `{"credentials":[{"principalId":"user","sha256":"bad"}]}`, valid + ` {}`, `{"credentials":[],"credentials":[]}`, `{"credentials":[],"token":"plaintext"}`, fmt.Sprintf(`{"credentials":[{"principalId":"user","sha256":"%s"},{"principalId":"other","sha256":"%s"}]}`, hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:]))} {
		put(t, path, config)
		if p, err := a.Authenticate(context.Background(), ports.AuthenticationEvidence{Material: token}); err == nil || p.ID != "" {
			t.Fatal("invalid config accepted")
		}
	}
	put(t, path, valid)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Authenticate(ctx, ports.AuthenticationEvidence{Material: token}); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := NewAuthentication(filepath.Join(t.TempDir(), "missing")).Authenticate(context.Background(), ports.AuthenticationEvidence{Material: token}); err == nil {
		t.Fatal("missing configuration allowed")
	}
}
func TestLocalGrantsReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	put(t, path, `{"grants":[{"principalId":"user","operation":"project.status","projectId":"p"}]}`)
	g := NewGrants(path)
	wanted := ports.Grant{PrincipalID: "user", Operation: "project.status", ProjectID: "p"}
	if got, found, err := g.FindExact(context.Background(), wanted); err != nil || !found || got != wanted {
		t.Fatal("exact grant not found")
	}
	for _, other := range []ports.Grant{{PrincipalID: "other", Operation: "project.status", ProjectID: "p"}, {PrincipalID: "user", Operation: "git.status", ProjectID: "p"}, {PrincipalID: "user", Operation: "project.status", ProjectID: "other"}} {
		if _, found, err := g.FindExact(context.Background(), other); err != nil || found {
			t.Fatal("non-exact grant")
		}
	}
	put(t, path, `{"grants":[]}`)
	if _, found, err := g.FindExact(context.Background(), wanted); err != nil || found {
		t.Fatal("revocation not reflected")
	}
	for _, config := range []string{`{}`, `null`, `{"grants":[{"principalId":"user","operation":"WRITE","projectId":"p"}]}`, `{"grants":[{"principalId":"*","operation":"project.status","projectId":"p"}]}`, `{"grants":[],"roles":[]}`, `{"grants":[],"grants":[]}`, `broken`} {
		put(t, path, config)
		if _, found, err := g.FindExact(context.Background(), wanted); err == nil || found {
			t.Fatal("invalid grants accepted")
		}
	}
	if _, found, err := NewGrants(filepath.Join(t.TempDir(), "missing")).FindExact(context.Background(), wanted); err == nil || found {
		t.Fatal("missing source accepted")
	}
}
