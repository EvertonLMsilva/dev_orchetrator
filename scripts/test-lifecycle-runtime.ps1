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
function Invoke-Docker($Arguments,$Timeout) {
    $dockerCalls.Add(($Arguments -join ' '))
    switch ($Arguments[0]) {
        ps { return $containerId }
        inspect { return (@(@{Id=$containerId;Config=@{Labels=@{'com.docker.compose.project'=$project;'com.docker.compose.service'='orchestrator'}};State=@{Running=$running;Health=@{Status='healthy'}}}) | ConvertTo-Json -Depth 6 -Compress) }
        stop { $script:running=$false; return '' }
        default { throw 'unexpected Docker operation' }
    }
}
Assert (Test-LifecycleOwned $state MCP) 'owned container'
$state.MCP=@{Id=('c'*64);Project=$project}
Assert (!(Test-LifecycleOwned $state MCP)) 'replaced container ownership'
Stop-OwnedComponent $state MCP
Assert (($dockerCalls -join ',') -notmatch 'stop ') 'replaced container stopped'
$state.MCP=@{Id=$containerId;Project=$project}
Stop-OwnedComponent $state MCP
Assert (!$running) 'owned container not stopped'
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
