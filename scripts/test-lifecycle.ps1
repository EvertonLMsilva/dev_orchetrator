$ErrorActionPreference = 'Stop'
. "$PSScriptRoot/lifecycle-core.ps1"
function Assert($Condition, $Message) { if (!$Condition) { throw $Message } }
function New-Fake {
    $script:f = @{ State=$null; Calls=[Collections.Generic.List[string]]::new(); Live=@{}; Identity=@{}; Fail=''; Invalid=$false; Locked=$false }
    return @{
        Lock={ if ($f.Locked) { throw 'concurrent' }; $f.Locked=$true; New-Object PSObject -Property @{} | Add-Member ScriptMethod Dispose { $script:f.Locked=$false } -PassThru }
        Load={ if ($f.Invalid) { throw 'invalid metadata' }; $f.State }
        New={ @{ Phase='STOPPED'; Attempted=@(); Instance=[guid]::NewGuid().ToString('N') } }
        Save={ param($s) $f.State=$s; $f.Calls.Add($s.Phase) }
        Validate={ $f.Calls.Add('validate'); if ($f.Fail -eq 'config') { throw 'config' } }
        Start={ param($s,$c) $f.Calls.Add("start:$c"); $f.Live[$c]=$true; $f.Identity[$c]=$s.Instance; if ($f.Fail -eq "start:$c") { throw 'start' } }
        Wait={ param($s,$c) $f.Calls.Add("wait:$c"); if ($f.Fail -eq "wait:$c") { throw [OperationCanceledException]::new() } }
        Owned={ param($s,$c) $s -and $f.Live[$c] -and $f.Identity[$c] -eq $s.Instance }
        Healthy={ param($s,$c) $f.Live[$c] -and $f.Fail -ne "health:$c" }
        Stop={ param($s,$c) $f.Calls.Add("stop:$c"); $f.Live[$c]=$false }
    }
}
$a=New-Fake
Assert ((Start-Lifecycle $a) -eq 'READY') 'startup'
Assert (($f.Calls -join ',') -match 'STARTING,start:MCP,STARTING,wait:MCP.*start:Development.*wait:Development.*start:Tunnel.*wait:Tunnel.*READY') 'order'
$before=$f.Calls.Count
try { Start-Lifecycle $a; throw 'accepted duplicate' } catch { Assert ($_.Exception.Message -ne 'accepted duplicate') 'duplicate' }
Assert ($before -eq $f.Calls.Count) 'duplicate effects'
Assert ((Get-LifecycleStatus $f.State $a) -eq 'READY') 'healthy status'
Assert ($before -eq $f.Calls.Count) 'status effects'
$f.Fail='health:Development'
Assert ((Get-LifecycleStatus $f.State $a) -eq 'DEGRADED') 'degraded'
$f.Identity.Tunnel='reused-pid'
Stop-Lifecycle $a | Out-Null
Assert ($f.Live.Tunnel) 'foreign process killed'
Assert (($f.Calls -join ',') -notmatch 'stop:Tunnel') 'ownership bypass'
foreach ($failure in 'start:MCP','start:Development','start:Tunnel','wait:MCP','wait:Development','wait:Tunnel') {
    $a=New-Fake; $f.Fail=$failure
    try { Start-Lifecycle $a; throw 'accepted failure' } catch { Assert ($_.Exception.Message -ne 'accepted failure') 'failure' }
    Assert ($f.State.Phase -eq 'FAILED') 'failure state'
    foreach ($c in $f.Live.Keys) { Assert (!$f.Live[$c]) 'rollback leaked owner' }
    Assert (!$f.Locked) 'lock leaked'
}
$a=New-Fake; $f.Fail='config'
try { Start-Lifecycle $a } catch {}
Assert (!$f.State -and $f.Live.Count -eq 0) 'invalid config effects'
$a=New-Fake; $f.Invalid=$true
try { Stop-Lifecycle $a } catch {}
Assert ($f.Calls.Count -eq 0) 'invalid metadata effects'
$a=New-Fake; $f.Locked=$true
try { Start-Lifecycle $a } catch {}
Assert ($f.Calls.Count -eq 0) 'concurrent effects'
$a=New-Fake
Assert ((Get-LifecycleStatus $null $a) -eq 'STOPPED') 'empty status'
Assert ($f.Calls.Count -eq 0) 'empty status effects'
Write-Output 'P11.2 lifecycle doubles PASS'
