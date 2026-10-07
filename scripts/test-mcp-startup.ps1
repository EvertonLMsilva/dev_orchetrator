$ErrorActionPreference = 'Stop'
$script:dockerCalls = 0
function docker { $script:dockerCalls++; throw 'Unexpected Docker call' }
$saved = $env:CONTROL_PLANE_API_KEY
try {
    $env:CONTROL_PLANE_API_KEY = ''
    $failed = $false
    try { & "$PSScriptRoot/mcp-runtime.ps1" } catch {
        if ($_.Exception.Message -ne 'MCP runtime prerequisite missing: CONTROL_PLANE_API_KEY') { throw }
        $failed = $true
    }
    if (!$failed -or $script:dockerCalls -ne 0) { throw 'Startup must fail before side effects' }
} finally { $env:CONTROL_PLANE_API_KEY = $saved }
Write-Output 'MCP7 safe startup failure PASS'

# Exercise lifecycle without Docker services or the real Tunnel.
$global:mcp7TestCalls = [Collections.Generic.List[string]]::new()
$global:mcp7TestFailUp = $false
function Start-Job {
    param($ArgumentList, $ScriptBlock)
    $a = @($ArgumentList[0])
    $line = $a -join ' '
    $global:mcp7TestCalls.Add($line)
    $code = 0
    if ($global:mcp7TestFailUp -and $line -match 'up -d') { $code = 1 }
    return @{ Code = $code; Output = '' }
}
function Wait-Job { param($Job, $Timeout) return $Job }
function Receive-Job { param($Job) return $Job }
function Stop-Job { param($Job) }
function Remove-Job { param($Job) }
function Invoke-WebRequest { param($Uri, [switch]$UseBasicParsing, $TimeoutSec) return @{ StatusCode = 200 } }
$temp = Join-Path ([IO.Path]::GetTempPath()) ('mcp7-test-' + [guid]::NewGuid())
New-Item -ItemType Directory $temp | Out-Null
$stub = Join-Path $temp 'tunnel.cmd'
Set-Content -LiteralPath $stub -Value '@exit /b 0'
$secretKeys = @('CONTROL_PLANE_API_KEY','MCP_CLIENT_TOKEN','CONTROL_PLANE_TUNNEL_ID')
$old = @{}
foreach ($key in $secretKeys) {
    $old[$key] = [Environment]::GetEnvironmentVariable($key)
    [Environment]::SetEnvironmentVariable($key, 'unit-test-only')
}
try {
    foreach ($failure in $false, $true) {
        $global:mcp7TestFailUp = $failure
        $global:mcp7TestCalls.Clear()
        $caught = $false
        try { & "$PSScriptRoot/mcp-runtime.ps1" -ConfigFile "$temp/tunnel.cmd" -Workspace $temp -TunnelExecutable $stub -LocalPort 18088 }
        catch { if (!$failure) { throw }; $caught = $true }
        if ($caught -ne $failure) { throw 'Unexpected startup outcome' }
        if (($global:mcp7TestCalls -join "`n") -match 'volume rm|down|--volumes') { throw 'Persistent storage destroyed' }
        if ($global:mcp7TestCalls[$global:mcp7TestCalls.Count - 1] -notmatch 'stop --timeout 120 orchestrator') { throw 'Missing owned runtime shutdown' }
        if (($global:mcp7TestCalls -join "`n") -notmatch '/provision.sh') { throw 'Missing security validation' }
    }
} finally {
    foreach ($key in $secretKeys) { [Environment]::SetEnvironmentVariable($key, $old[$key]) }
    # Only test-owned files; no recursive deletion.
    Remove-Item -LiteralPath $stub
    Remove-Item -LiteralPath $temp
}
Write-Output 'MCP7 startup/shutdown and startup rollback contracts PASS'
