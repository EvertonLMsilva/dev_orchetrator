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
        Start={ param($s,$c) $f.Calls.Add("start:$c"); $f.Live[$c]=$true; $f.Identity[$c]=$s.Instance; if ($f.Fail -eq "start:$c") { throw 'Authorization: Bearer fake-secret-sentinel provider-output' } }
        Wait={ param($s,$c) $f.Calls.Add("wait:$c"); if ($f.Fail -eq "wait:$c") { throw [OperationCanceledException]::new() } }
        Owned={ param($s,$c) $s -and $f.Live[$c] -and $f.Identity[$c] -eq $s.Instance }
        Healthy={ param($s,$c) $f.Live[$c] -and $f.Fail -ne "health:$c" }
        Stop={ param($s,$c) if (!$f.Identity.ContainsKey($c) -or $f.Identity[$c] -ne $s.Instance) { return }; $f.Calls.Add("stop:$c"); $f.Live[$c]=$false }
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
$a=New-Fake
Start-Lifecycle $a | Out-Null
$f.Live.MCP=$false; $f.Identity.Development='foreign-owner'; $f.Calls.Clear()
Stop-Lifecycle $a | Out-Null
Assert ($f.Calls.Contains('stop:MCP')) 'stopped Docker resource skipped during cleanup'
Assert (!$f.Calls.Contains('stop:Development')) 'foreign Docker resource cleaned'
foreach ($failure in 'start:MCP','start:Development','start:Tunnel','wait:MCP','wait:Development','wait:Tunnel') {
    $a=New-Fake; $f.Fail=$failure
    try { Start-Lifecycle $a; throw 'accepted failure' } catch {
        Assert ($_.Exception.Message -ne 'accepted failure') 'failure'
        $expected=$failure.Split(':')
        Assert ($_.Exception.Data['LifecycleStage'] -ceq $expected[0].ToUpperInvariant()) 'missing sanitized failure stage'
        Assert ($_.Exception.Data['LifecycleComponent'] -ceq $expected[1]) 'missing sanitized failure component'
        Assert ($_.Exception.ToString() -notmatch 'fake-secret-sentinel|Authorization|provider-output') 'startup diagnostic leaked secret/provider output'
    }
    Assert ($f.State.Phase -eq 'FAILED') 'failure state'
    foreach ($c in $f.Live.Keys) { Assert (!$f.Live[$c]) 'rollback leaked owner' }
    Assert (!$f.Locked) 'lock leaked'
}
foreach ($component in 'MCP','Development','Tunnel') {
    $a=New-Fake; $f.Fail="health:$component"
    try { Start-Lifecycle $a; throw 'accepted aggregate failure' } catch {
        Assert ($_.Exception.Data['LifecycleStage'] -ceq 'AGGREGATE') 'missing aggregate failure stage'
        Assert ($_.Exception.Data['LifecycleComponent'] -ceq $component) 'wrong aggregate failure component'
    }
    foreach ($c in $f.Live.Keys) { Assert (!$f.Live[$c]) 'aggregate rollback leaked owner' }
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
