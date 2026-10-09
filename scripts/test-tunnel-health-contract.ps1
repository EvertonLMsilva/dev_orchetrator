#Requires -Version 7.0
$ErrorActionPreference='Stop'
. "$PSScriptRoot/lifecycle-runtime.ps1"
function Assert($ok,$message) { if (!$ok) { throw $message } }
function Get-OwnedTunnel($s) { @{Id=123;StartTime=[DateTime]'2026-01-01T00:00:00Z'} }
function Get-NetTCPConnection { param($OwningProcess,$State,$ErrorAction) @{LocalAddress='127.0.0.1';LocalPort=18099} }
# Shape verified against bundled runtime: successful polling omits http_status.
$fixture='{"live":true,"ready":true,"runtime":{"lifecycle":"running"},"components":{"control-plane":{"status":"ok","state":"polling","details":{"consecutive_failures":0,"last_success":"2026-01-01T00:01:40Z"}}}}'
$script:health=$fixture | ConvertFrom-Json -AsHashtable
$script:metrics='commands_poll_last_successful_timestamp_seconds 1767225700'
function Invoke-WebRequest { param($Uri,$TimeoutSec,$MaximumRedirection) if($Uri -match '/metrics$'){@{StatusCode=200;Content=$metrics}}else{@{StatusCode=200;Content=($health | ConvertTo-Json -Depth 8)}} }
Assert (Test-LifecycleHealthy @{} Tunnel) 'successful runtime response without optional http_status rejected'
foreach($status in @(401,403,500,$null,'200',200.5)) {
 $script:health=$fixture | ConvertFrom-Json -AsHashtable
 $health.components['control-plane'].details.http_status=$status
 Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'invalid or failed HTTP status accepted'
}
foreach($path in @('live','ready','runtime','runtime.lifecycle','components','components.control-plane','components.control-plane.status','components.control-plane.state','components.control-plane.details','components.control-plane.details.consecutive_failures')) {
 $script:health=$fixture | ConvertFrom-Json -AsHashtable;$parts=$path.Split('.');$node=$health
 for($i=0;$i -lt $parts.Count-1;$i++){$node=$node[$parts[$i]]}
 $node.Remove($parts[-1])
 Assert (!(Test-LifecycleHealthy @{} Tunnel)) "missing mandatory $path accepted"
}
$script:health=$fixture | ConvertFrom-Json -AsHashtable
$health.components['control-plane'].status='degraded'
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'degraded without http_status accepted'
$health.components['control-plane'].status='ok';$health.components['control-plane'].state='backoff'
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'backoff without http_status accepted'
$health.components['control-plane'].state='polling';$health.components['control-plane'].details.consecutive_failures=1
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'failed poll without http_status accepted'
$health.components['control-plane'].details.consecutive_failures=0;$script:metrics='commands_poll_last_successful_timestamp_seconds 0'
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'missing authenticated poll accepted'
$script:metrics='commands_poll_last_successful_timestamp_seconds 1767225700';$health.components['control-plane'].details.http_status=200
Assert (Test-LifecycleHealthy @{} Tunnel) 'explicit successful HTTP rejected'
Write-Output 'Tunnel HealthContract PASS (runtime shape, mandatory fields, 401/403/degraded, authenticated polling)'
