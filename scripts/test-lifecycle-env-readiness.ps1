#Requires -Version 7.0
$ErrorActionPreference='Stop'
. "$PSScriptRoot/lifecycle-runtime.ps1"
function Assert($ok,$message) { if (!$ok) { throw $message } }
function Get-OwnedTunnel($s) { @{Id=123;StartTime=[DateTime]'2026-01-01T00:00:00Z'} }
function Get-NetTCPConnection { param($OwningProcess,$State,$ErrorAction) @{LocalAddress='127.0.0.1';LocalPort=18099} }
$script:health=@{live=$true;ready=$true;runtime=@{lifecycle='running'};components=@{'control-plane'=@{status='degraded';state='backoff';details=@{http_status=401;consecutive_failures=321}}}}
$script:metrics='commands_poll_last_successful_timestamp_seconds 0'
function Invoke-WebRequest { param($Uri,$TimeoutSec,$MaximumRedirection) if ($Uri -match '/metrics$') { @{StatusCode=200;Content=$metrics} } else { @{StatusCode=200;Content=($health | ConvertTo-Json -Depth 8)} } }
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'RED: live tunnel with 401/degraded must not be healthy'
$health.components['control-plane'].status='ok';$health.components['control-plane'].state='polling';$health.components['control-plane'].details.http_status=200;$health.components['control-plane'].details.consecutive_failures=0
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'zero successful polls accepted'
$script:metrics='commands_poll_last_successful_timestamp_seconds 1767225700'
Assert (Test-LifecycleHealthy @{} Tunnel) 'healthy authenticated polling rejected'
$script:metrics='commands_poll_last_successful_timestamp_seconds 1'
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'stale success accepted'
$script:metrics='commands_poll_last_successful_timestamp_seconds 1767225700'
$health.components['control-plane'].status='degraded'
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'old poll masks degraded control plane'
$health.components['control-plane'].status='ok';$health.components['control-plane'].details.http_status=401
Assert (!(Test-LifecycleHealthy @{} Tunnel)) '401 accepted with successful historical polling'
$health.components['control-plane'].details.http_status=200
$health.components['control-plane'].details.consecutive_failures=1
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'poll failure accepted'
$health.components['control-plane'].details.consecutive_failures=0
$health.Remove('components')
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'missing health evidence accepted'
$saved=@{}; foreach ($key in 'CONTROL_PLANE_API_KEY','CONTROL_PLANE_TUNNEL_ID') {$saved[$key]=[Environment]::GetEnvironmentVariable($key);[Environment]::SetEnvironmentVariable($key,[NullString]::Value)}
$path=[IO.Path]::GetTempFileName()
try {
 [IO.File]::WriteAllText($path,"CONTROL_PLANE_API_KEY=test-only-key`nCONTROL_PLANE_TUNNEL_ID=tunnel_test")
 Import-LifecycleEnvironment $path
 Assert ($env:CONTROL_PLANE_API_KEY -ceq 'test-only-key') '.env not loaded'
 $env:CONTROL_PLANE_API_KEY='existing-test-only'
 Import-LifecycleEnvironment $path
 Assert ($env:CONTROL_PLANE_API_KEY -ceq 'existing-test-only') 'environment precedence'
 Import-LifecycleEnvironment "$path.absent"
 Assert ($env:CONTROL_PLANE_API_KEY -ceq 'existing-test-only') 'environment-only startup'
 [IO.File]::WriteAllText($path,("# literal values only`nCONTROL_PLANE_API_KEY='test-only-key'`nCONTROL_PLANE_TUNNEL_ID="+'"tunnel_test"'))
 Import-LifecycleEnvironment $path
 foreach ($bad in @('CONTROL_PLANE_API_KEY=', 'OTHER_KEY=no', 'MCP_RUNTIME_AUTHORIZATION=no', "CONTROL_PLANE_API_KEY=one`nCONTROL_PLANE_API_KEY=two", 'CONTROL_PLANE_API_KEY=$(Get-Process)')) {
  [IO.File]::WriteAllText($path,$bad);$failed=$false
  try { Import-LifecycleEnvironment $path } catch {$failed=$true;Assert ($_.Exception.ToString() -notmatch 'existing-test-only|Get-Process') 'secret/content in diagnostic'}
  Assert $failed 'unsafe env accepted'
  Assert ($env:CONTROL_PLANE_API_KEY -ceq 'existing-test-only') 'partial mutation'
 }
 foreach ($key in $saved.Keys) {[Environment]::SetEnvironmentVariable($key,[NullString]::Value)}
 [IO.File]::WriteAllText($path,'# no secrets')
 $failed=$false;try {Import-LifecycleEnvironment $path} catch {$failed=$true}
 Assert $failed 'absent secrets accepted'
 [Environment]::SetEnvironmentVariable('CONTROL_PLANE_API_KEY','')
 [Environment]::SetEnvironmentVariable('CONTROL_PLANE_TUNNEL_ID','existing-id')
 [IO.File]::WriteAllText($path,"CONTROL_PLANE_API_KEY=test-only-key`nCONTROL_PLANE_TUNNEL_ID=tunnel_test")
 $failed=$false;try {Import-LifecycleEnvironment $path} catch {$failed=$true}
 Assert $failed 'explicitly empty environment silently overridden'
} finally {foreach ($key in $saved.Keys) {[Environment]::SetEnvironmentVariable($key,$saved[$key])};Remove-Item -LiteralPath $path}
Write-Output 'Lifecycle env/readiness PASS (no live processes)'
