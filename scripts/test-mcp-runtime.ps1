$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$env:MCP_CONFIG_FILE = Join-Path $root '.runtime/mcp-real/orchestrator.json'
$env:MCP_PROJECT_WORKSPACE = $root
$raw = & docker compose -f "$root/deploy/mcp-read-only.compose.yml" config --format json
if ($LASTEXITCODE) { throw 'Compose config failed' }
$config = ($raw -join "`n") | ConvertFrom-Json
$service = $config.services.orchestrator
if ($service.build.target -ne 'runtime' -or !$service.read_only) { throw 'Runtime/rootfs contract' }
if ($service.cap_drop -notcontains 'ALL' -or $service.security_opt -notcontains 'no-new-privileges:true') { throw 'Privilege contract' }
$security = $service.volumes | Where-Object target -eq '/run/mcp/security'
if ($security.type -ne 'volume' -or !$security.read_only) { throw 'Linux security volume contract' }
if (!$config.volumes.security.external -or !$config.volumes.operational.external) { throw 'Persistent volume contract' }
if ($service.ports[0].host_ip -ne '127.0.0.1') { throw 'Loopback contract' }
if ($service.volumes.target -contains '/var/run/docker.sock') { throw 'Docker socket exposed' }
Write-Output 'MCP7 Compose contracts PASS'
