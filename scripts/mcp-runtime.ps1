# Foreground Windows operation. Ctrl+C shuts down its Compose runtime.
[CmdletBinding()]
param(
    [string]$ConfigFile = (Join-Path (Split-Path $PSScriptRoot -Parent) '.runtime/mcp-real/orchestrator.json'),
    [string]$Workspace = (Split-Path $PSScriptRoot -Parent),
    [string]$SecuritySource,
    [ValidateRange(1,65535)][int]$LocalPort = 8080,
    [string]$TunnelExecutable = (Join-Path (Split-Path $PSScriptRoot -Parent) 'tunnel-client/tunnel-client.exe'),
    [switch]$Managed,
    [ValidatePattern('^dev-orchestrator-[a-f0-9]{32}-mcp$')][string]$ManagedProject
)
$ErrorActionPreference = 'Stop'
foreach ($name in 'CONTROL_PLANE_API_KEY', 'MCP_CLIENT_TOKEN', 'CONTROL_PLANE_TUNNEL_ID') {
    if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name))) {
        throw "MCP runtime prerequisite missing: $name"
    }
}
foreach ($file in $ConfigFile, $TunnelExecutable) {
    if (!(Test-Path -LiteralPath $file -PathType Leaf)) { throw 'MCP runtime required file missing' }
}
if (!(Test-Path -LiteralPath $Workspace -PathType Container)) { throw 'MCP runtime workspace missing' }
if ($SecuritySource) {
    foreach ($name in 'auth.json', 'grants.json') {
        if (!(Test-Path -LiteralPath (Join-Path $SecuritySource $name) -PathType Leaf)) { throw 'MCP security source missing' }
    }
    $SecuritySource = (Resolve-Path -LiteralPath $SecuritySource).Path
}
Get-Command docker -ErrorAction Stop | Out-Null
$root = Split-Path $PSScriptRoot -Parent
if ($Managed -and !$ManagedProject) { throw 'Managed MCP requires unique project identity' }
$project = if ($Managed) { $ManagedProject } else { 'dev-orchestrator-mcp' }
$compose = @('compose', '-p', $project, '-f', "$root/deploy/mcp-read-only.compose.yml")
$keys = @('MCP_CONFIG_FILE','MCP_PROJECT_WORKSPACE','MCP_LOCAL_PORT','MCP_SECURITY_VOLUME','MCP_OPERATIONAL_VOLUME','MCP_RUNTIME_IMAGE','MCP_RUNTIME_AUTHORIZATION')
$saved = @{}
foreach ($key in $keys) { $saved[$key] = [Environment]::GetEnvironmentVariable($key) }
$env:MCP_CONFIG_FILE = (Resolve-Path -LiteralPath $ConfigFile).Path
$env:MCP_PROJECT_WORKSPACE = (Resolve-Path -LiteralPath $Workspace).Path
$env:MCP_LOCAL_PORT = "$LocalPort"
$env:MCP_SECURITY_VOLUME = 'dev-orchestrator-mcp-security'
$env:MCP_OPERATIONAL_VOLUME = 'dev-orchestrator-mcp-state'
$env:MCP_RUNTIME_IMAGE = 'dev-orchestrator-mcp-read-only'
. "$PSScriptRoot/runtime-docker.ps1"
$started = $false
$detached = $false
try {
    Invoke-Docker @('info') | Out-Null
    Invoke-Docker ($compose + @('config','--quiet')) | Out-Null
    if (Invoke-Docker ($compose + @('ps','-q'))) { throw 'MCP Compose already owns a running container; stop it before startup' }
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,$LocalPort)
    try { $listener.Start() } catch { throw 'MCP loopback port unavailable' } finally { $listener.Stop() }
    Write-Output 'Building MCP runtime (bounded to 600 seconds)'
    Invoke-Docker ($compose + @('build','orchestrator')) 600 | Out-Null
    foreach ($volume in $env:MCP_SECURITY_VOLUME, $env:MCP_OPERATIONAL_VOLUME) {
        Invoke-Docker @('volume','create',$volume) | Out-Null
    }
    $helper = @('run','--rm','--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges:true',
        '--mount',"type=volume,source=$env:MCP_SECURITY_VOLUME,target=/security",
        '--mount',"type=bind,source=$PSScriptRoot/provision-mcp-security.sh,target=/provision.sh,readonly")
    if ($SecuritySource) {
        $helper += @('--mount',"type=bind,source=$SecuritySource,target=/source,readonly",'-e','SECURITY_SOURCE=/source')
    }
    Invoke-Docker ($helper + @('--entrypoint','sh','dev-orchestrator-mcp-read-only','/provision.sh')) | Out-Null
    Invoke-Docker @('run','--rm','--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges:true',
        '--mount',"type=volume,source=$env:MCP_OPERATIONAL_VOLUME,target=/state",
        '--mount',"type=bind,source=$PSScriptRoot/provision-mcp-state.sh,target=/provision-state.sh,readonly",'--entrypoint','sh',
        'dev-orchestrator-mcp-read-only','/provision-state.sh') | Out-Null
    $started = $true
    Invoke-Docker ($compose + @('up','-d','--no-build','--wait','--wait-timeout','90','orchestrator')) 120 | Out-Null
    try { $response = Invoke-WebRequest "http://127.0.0.1:$LocalPort/readyz" -UseBasicParsing -TimeoutSec 5 }
    catch { throw 'MCP readiness request failed' }
    if ($response.StatusCode -ne 200) { throw 'MCP readiness failed' }
    if ($Managed) { $detached = $true; return }
    Write-Output 'MCP ready. Starting foreground Tunnel; Ctrl+C stops this operation. Tunnel output is suppressed.'
    $env:MCP_RUNTIME_AUTHORIZATION = 'Bearer ' + $env:MCP_CLIENT_TOKEN
    # Explicit references keep credentials out of command lines. Disable file/raw logging.
    & $TunnelExecutable run --control-plane.api-key env:CONTROL_PLANE_API_KEY `
        --health.listen-addr '127.0.0.1:0' `
        --mcp.server-url "http://127.0.0.1:$LocalPort/mcp" `
        --mcp.extra-headers 'Authorization: env:MCP_RUNTIME_AUTHORIZATION' `
        --mcp.discovery-extra-headers 'Authorization: env:MCP_RUNTIME_AUTHORIZATION' `
        --mcp.startup-wait-timeout 30s `
        --log.http-raw-unsafe=false --log.file stdout --log.level error *> $null
    if ($LASTEXITCODE -ne 0) { throw 'MCP Tunnel exited unsuccessfully (output suppressed)' }
} finally {
    try {
        if ($started -and !$detached) { Invoke-Docker ($compose + @('stop','--timeout','120','orchestrator')) 150 | Out-Null }
    } finally {
        foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key]) }
    }
}
