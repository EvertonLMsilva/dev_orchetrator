package composition

import (
	"dev-orchestrator/internal/adapters/localsecurity"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/ports"
	"errors"
	"os"
	"path/filepath"
)

// MCPReadConfig contains trusted administrative paths, not Channel arguments.
// Security files must reside outside project workspaces, in protected directories.
type MCPReadConfig struct{ AuthenticationFile, GrantsFile, AuditFile string }

// NewMCPReadRuntime builds an internal runtime only; it installs no server or
// transport and constructs no Planner/Executor. READ capability ports are trusted
// existing composition dependencies; callers receive only the gated Query API.
func NewMCPReadRuntime(c MCPReadConfig, p ports.ProjectRepository, t ports.TaskRepository, agent *application.LocalAgentDispatcher) (*application.SecureReadRuntime, error) {
	fail := errors.New("secure READ runtime configuration unavailable")
	if p == nil || c.AuthenticationFile == c.GrantsFile || c.AuthenticationFile == c.AuditFile || c.GrantsFile == c.AuditFile || !filepath.IsAbs(c.AuditFile) || filepath.Clean(c.AuditFile) != c.AuditFile {
		return nil, fail
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(c.AuditFile))
	if err != nil || parent != filepath.Dir(c.AuditFile) {
		return nil, fail
	}
	if localsecurity.Validate(c.AuthenticationFile, c.GrantsFile) != nil {
		return nil, fail
	}
	if info, err := os.Lstat(c.AuditFile); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fail
		}
	} else if !os.IsNotExist(err) {
		return nil, fail
	}
	return application.NewSecureReadRuntime(localsecurity.NewAuthentication(c.AuthenticationFile), localsecurity.NewGrants(c.GrantsFile), localsecurity.NewAudit(c.AuditFile), p, t, agent), nil
}
