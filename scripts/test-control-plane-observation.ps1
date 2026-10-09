#Requires -Version 7.0
$ErrorActionPreference='Stop'
. "$PSScriptRoot/lifecycle-runtime.ps1"
function Assert($ok,$message) { if (!$ok) { throw $message } }
function Get-OwnedTunnel($s) { @{Id=123;StartTime=[DateTime]'2026-01-01T00:00:00Z'} }
function Get-NetTCPConnection { param($OwningProcess,$State,$ErrorAction) @{LocalAddress='127.0.0.1';LocalPort=18099} }
$script:health='{"live":true,"ready":true,"runtime":{"lifecycle":"running"},"components":{"control-plane":{"status":"limited","state":"polling","details":{"consecutive_failures":0,"last_success":"2026-01-01T00:01:40Z","secret":"secret-sentinel"}}}}' | ConvertFrom-Json -AsHashtable
function Invoke-WebRequest { param($Uri,$TimeoutSec,$MaximumRedirection) @{StatusCode=200;Content=($health | ConvertTo-Json -Depth 8)} }
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'limited accepted'
$d=$script:TunnelReadinessDiagnostic
Assert ($d.Condition -ceq 'ControlPlaneStatus') 'predicate changed'
Assert ($d.ControlPlane.Status -ceq 'limited' -and $d.ControlPlane.State -ceq 'polling' -and $d.ControlPlane.ConsecutiveFailures -eq 0) 'observed control-plane values missing'
Assert ($d.ControlPlane.LastSuccessUnixSeconds -eq 1767225700 -and $d.ControlPlane.TunnelStartedUnixSeconds -eq 1767225600) 'poll freshness observation missing'
Assert ($null -eq $d.HttpStatus) 'invented HTTP status'
$script:LifecycleDirectory=Join-Path ([IO.Path]::GetTempPath()) ('control-observation-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $LifecycleDirectory | Out-Null
Write-TunnelReadinessDiagnostic @{Instance=('c'*32)} ([Diagnostics.Stopwatch]::StartNew()) 'Rejected'
$record=Get-Content -LiteralPath (Join-Path $LifecycleDirectory ('tunnel-readiness-'+('c'*32)+'.jsonl')) | ConvertFrom-Json
Assert ($record.ControlPlane.Status -ceq 'limited') 'safe observation not persisted'
$health.components['control-plane'].status='secret-sentinel'
$health.components['control-plane'].state='https://user:password@example.invalid'
$health.components['control-plane'].details.last_success='secret-sentinel'
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'unknown status accepted'
$d=$script:TunnelReadinessDiagnostic
Assert ($d.ControlPlane.Status -ceq 'UNRECOGNIZED' -and $d.ControlPlane.State -ceq 'UNRECOGNIZED' -and $null -eq $d.ControlPlane.LastSuccessUnixSeconds) 'unsafe provider values retained'
Assert (($d | ConvertTo-Json -Depth 5) -notmatch 'secret-sentinel|password|https:') 'sensitive provider data exposed'
$health.Remove('components')
Assert (!(Test-LifecycleHealthy @{} Tunnel)) 'missing mandatory contract accepted'
Assert ($null -eq $script:TunnelReadinessDiagnostic.ControlPlane) 'stale control observation retained'
Write-Output 'Control Plane observation PASS (safe projection, persistence, unchanged rejection, no stale values)'
