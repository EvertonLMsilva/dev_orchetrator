# No real Docker, gateway, provider or Tunnel. Windows metadata + adapter tests.
#Requires -Version 7.0
$ErrorActionPreference='Stop'
. "$PSScriptRoot/lifecycle-runtime.ps1"
function Assert($condition,$message) { if (!$condition) { throw $message } }
$script:LifecycleDirectory=Join-Path ([IO.Path]::GetTempPath()) ('p11-2-'+[guid]::NewGuid().ToString('N'))
$script:LifecycleFile=Join-Path $LifecycleDirectory 'owner.bin'
Initialize-LifecycleDirectory
$state=@{Version=1;Instance=('a'*32);Phase='STARTING';Port=18088;Attempted=@('MCP');MCP=$null;Development=$null;Tunnel=$null}
Write-LifecycleState $state
$loaded=Read-LifecycleState
Assert ($loaded.Instance -eq $state.Instance) 'metadata roundtrip'
$lock=[IO.FileStream]::new((Join-Path $LifecycleDirectory 'lifecycle.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)
try {
    $rejected=$false
    try { $other=[IO.FileStream]::new($lock.Name,[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None); $other.Dispose() } catch { $rejected=$true }
    Assert $rejected 'concurrent lock accepted'
} finally { $lock.Dispose() }
$script:dockerCalls=[Collections.Generic.List[string]]::new()
$script:containerId='b'*64
$script:project="dev-orchestrator-$($state.Instance)-mcp"
$script:running=$true
$script:containerExists=$true
$script:changeOwnershipOnStop=$false
$script:failRemove=$false
function Invoke-Docker($Arguments,$Timeout) {
    $dockerCalls.Add(($Arguments -join ' '))
    switch ($Arguments[0]) {
        ps { if ($containerExists) { return $containerId }; return '' }
        inspect { return (@(@{Id=$containerId;Config=@{Labels=@{'com.docker.compose.project'=$script:project;'com.docker.compose.service'='orchestrator'}};State=@{Running=$running;Health=@{Status='healthy'}}}) | ConvertTo-Json -Depth 6 -Compress) }
        stop { Assert ($Timeout -eq 45) 'unbounded stop'; $script:running=$false; if ($changeOwnershipOnStop) { $script:project='foreign-project' }; return '' }
        rm { Assert ($Arguments.Count -eq 2 -and $Arguments[1] -ceq $containerId -and !$running -and $Timeout -eq 45) 'unsafe container removal'; if ($failRemove) { throw 'fake removal failure' }; $script:containerExists=$false; return '' }
        default { throw 'unexpected Docker operation' }
    }
}
Assert (Test-LifecycleOwned $state MCP) 'owned container'
$state.MCP=@{Id=('c'*64);Project=$project}
Assert (!(Test-LifecycleOwned $state MCP)) 'replaced container ownership'
Stop-OwnedComponent $state MCP
Assert (($dockerCalls -join ',') -notmatch 'stop |rm ') 'replaced container stopped or removed'
$state.MCP=@{Id=$containerId;Project=$project}
Stop-OwnedComponent $state MCP
Assert (!$running) 'owned container not stopped'
Assert (!$containerExists) 'owned container not removed'
$before=$dockerCalls.Count
Stop-OwnedComponent $state MCP
Assert (($dockerCalls | Select-Object -Skip $before) -notmatch '^stop |^rm ') 'absent container cleanup not idempotent'
$script:containerExists=$true
$before=$dockerCalls.Count
Stop-OwnedComponent $state MCP
Assert (!$containerExists) 'already stopped owned container not removed'
Assert (($dockerCalls | Select-Object -Skip $before) -notmatch '^stop ') 'already stopped container stopped again'
$script:containerExists=$true; $script:running=$true; $script:changeOwnershipOnStop=$true
$before=$dockerCalls.Count
Stop-OwnedComponent $state MCP
Assert ($containerExists) 'container removed after ownership changed during stop'
Assert (($dockerCalls | Select-Object -Skip $before) -notmatch '^rm ') 'ownership not revalidated after stop'
$script:project=$state.MCP.Project; $script:changeOwnershipOnStop=$false; $script:failRemove=$true
$rejected=$false
try { Stop-OwnedComponent $state MCP } catch { $rejected=$true }
Assert ($rejected -and $containerExists) 'removal failure reported as success'
Assert (($dockerCalls -join ',') -notmatch 'volume rm|down|prune') 'data destruction'
$p=Get-Process -Id $PID
$state.Tunnel=@{Pid=$PID;Started='1';Executable=$p.MainModule.FileName}
Assert ($null -eq (Get-OwnedTunnel $state)) 'pid reuse test'
Assert (!(Test-LifecycleOwned $state Tunnel)) 'pid reuse accepted'
Stop-OwnedComponent $state Tunnel
$state.Tunnel=$null
Write-LifecycleState $state
$bytes=[IO.File]::ReadAllBytes($LifecycleFile); $index=[int]($bytes.Length/2); $bytes[$index]=$bytes[$index] -bxor 1; [IO.File]::WriteAllBytes($LifecycleFile,$bytes)
$rejected=$false
try { Read-LifecycleState | Out-Null } catch { $rejected=$true }
Assert $rejected 'tampered metadata accepted'
$state.Phase='INVALID'; Write-LifecycleState $state
$rejected=$false
try { Read-LifecycleState | Out-Null } catch { $rejected=$true }
Assert $rejected 'invalid schema accepted'
$state.Phase='READY'; Write-LifecycleState $state
$rejected=$false
try { Read-LifecycleState | Out-Null } catch { $rejected=$true }
Assert $rejected 'incomplete READY metadata accepted'
$state.Phase='STARTING'; $state.Version='1'; Write-LifecycleState $state
$rejected=$false
try { Read-LifecycleState | Out-Null } catch { $rejected=$true }
Assert $rejected 'malformed version accepted'
$target=Join-Path $LifecycleDirectory 'target'
New-Item -ItemType Directory -Path $target | Out-Null
$alias=Join-Path $LifecycleDirectory 'alias'
New-Item -ItemType Junction -Path $alias -Target $target | Out-Null
$rejected=$false
try { Assert-SafePath (Join-Path $alias 'owner.bin') } catch { $rejected=$true }
Assert $rejected 'reparse parent accepted'
Write-Output 'P11.2 Windows ownership/metadata doubles PASS (test evidence retained in temp)'
