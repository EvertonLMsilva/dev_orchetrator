#Requires -Version 7.0
$ErrorActionPreference='Stop'
. "$PSScriptRoot/lifecycle-runtime.ps1"
function Assert($ok,$message) { if (!$ok) { throw $message } }
function Get-OwnedTunnel($s) { @{Id=123;StartTime=[DateTime]'2026-01-01T00:00:00Z'} }
function Get-NetTCPConnection { param($OwningProcess,$State,$ErrorAction) @{LocalAddress='127.0.0.1';LocalPort=18099} }
$script:mode='auth'
function Invoke-WebRequest {
 param($Uri,$TimeoutSec,$MaximumRedirection)
 if ($mode -eq 'unreachable') { throw [Net.Http.HttpRequestException]::new('secret-token https://user:password@example.invalid') }
 if ($mode -eq 'timeout') { throw [TimeoutException]::new('secret-token') }
 @{StatusCode=200;Content='{"live":true,"ready":false,"runtime":{"lifecycle":"running"},"components":{"control-plane":{"status":"degraded","state":"backoff","details":{"http_status":401,"consecutive_failures":2,"secret":"secret-token"}}}}'}
}
$script:LifecycleDirectory=Join-Path ([IO.Path]::GetTempPath()) ('tunnel-diagnostics-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $LifecycleDirectory | Out-Null
$failures=@()
foreach ($case in @(@{Mode='auth';Condition='ControlPlaneHttpStatus';Http=401;Exception=$null},@{Mode='timeout';Condition='HealthRequest';Http=$null;Exception='System.TimeoutException'},@{Mode='unreachable';Condition='HealthRequest';Http=$null;Exception='System.Net.Http.HttpRequestException'})) {
 try {
  $script:mode=$case.Mode
  Assert (!(Test-LifecycleHealthy @{} Tunnel)) "$mode accepted"
  Assert ($script:TunnelReadinessDiagnostic.Condition -ceq $case.Condition) "$mode condition missing"
  Assert ($script:TunnelReadinessDiagnostic.HttpStatus -eq $case.Http) "$mode HTTP missing"
  Assert ($script:TunnelReadinessDiagnostic.ExceptionClass -ceq $case.Exception) "$mode exception missing"
  Write-TunnelReadinessDiagnostic @{Instance=('a'*32)} ([Diagnostics.Stopwatch]::StartNew()) 'Timeout'
  $record=Get-Content -LiteralPath (Join-Path $LifecycleDirectory ('tunnel-readiness-'+('a'*32)+'.jsonl')) -Tail 1 | ConvertFrom-Json
  Assert ($record.Stage -ceq 'WAIT' -and $record.Component -ceq 'Tunnel' -and $record.Outcome -ceq 'Timeout' -and $record.ElapsedMilliseconds -ge 0) 'timeout context missing'
  Assert ($record.Condition -ceq $case.Condition) 'last rejection lost'
  Assert (($record | ConvertTo-Json) -notmatch 'secret-token|password|https:|headers') 'sensitive evidence persisted'
 } catch { $failures += "$($case.Mode): $($_.Exception.Message)" }
}
Assert ($failures.Count -eq 0) ($failures -join '; ')
$waitAst=(Get-Command Invoke-LifecycleCommand).ScriptBlock.Ast.Find({param($node) $node -is [Management.Automation.Language.ScriptBlockExpressionAst] -and $node.Extent.Text -match 'Tunnel authenticated readiness timed out'},$true)
function Test-LifecycleOwned($s,$c) { $true }
$script:mode='unreachable';$timedOut=$false
try { & $waitAst.ScriptBlock.GetScriptBlock() @{Instance=('b'*32)} Tunnel } catch { $timedOut=$_.Exception.Message -ceq 'Tunnel authenticated readiness timed out' }
Assert $timedOut 'polling timeout not raised'
$record=Get-Content -LiteralPath (Join-Path $LifecycleDirectory ('tunnel-readiness-'+('b'*32)+'.jsonl')) -Tail 1 | ConvertFrom-Json
Assert ($record.Outcome -ceq 'Timeout' -and $record.Condition -ceq 'HealthRequest' -and $record.ExceptionClass -ceq 'System.Net.Http.HttpRequestException' -and $record.ElapsedMilliseconds -ge 30000) 'polling lost final rejection or timeout context'
Write-Output 'Tunnel readiness diagnostics PASS (auth, timeout, unreachable; evidence retained in temp)'
