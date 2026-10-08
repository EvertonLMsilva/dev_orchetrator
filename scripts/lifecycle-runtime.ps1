#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. "$PSScriptRoot/lifecycle-core.ps1"
. "$PSScriptRoot/runtime-docker.ps1"
$script:LifecycleRoot = Split-Path $PSScriptRoot -Parent
$script:LifecycleDirectory = Join-Path $LifecycleRoot '.runtime/lifecycle'
$script:LifecycleFile = Join-Path $LifecycleDirectory 'owner.bin'
function Assert-SafePath([string]$Path, [switch]$Leaf) {
    if (![IO.Path]::IsPathFullyQualified($Path)) { throw 'Lifecycle requires absolute paths' }
    $current = [IO.Path]::GetFullPath($Path)
    while ($current) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -Force -LiteralPath $current
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Lifecycle reparse path rejected' }
        }
        $current = [IO.Path]::GetDirectoryName($current)
    }
    if ($Leaf -and !(Test-Path -LiteralPath $Path -PathType Leaf)) { throw 'Lifecycle required file unavailable' }
}
function Assert-LifecycleDirectory {
    Assert-SafePath $LifecycleDirectory
    if (!(Test-Path -LiteralPath $LifecycleDirectory -PathType Container)) { throw 'Lifecycle directory unavailable' }
    $acl = Get-Acl -LiteralPath $LifecycleDirectory
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    foreach ($rule in $acl.Access) {
        $identity = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($rule.AccessControlType -eq 'Allow' -and $identity -notin @($sid,'S-1-5-18','S-1-5-32-544')) { throw 'Lifecycle directory must be private' }
    }
    if (!$acl.AreAccessRulesProtected) { throw 'Lifecycle directory inheritance must be disabled' }
}
function Initialize-LifecycleDirectory {
    if (!(Test-Path -LiteralPath $LifecycleDirectory)) {
        Assert-SafePath $LifecycleDirectory
        New-Item -ItemType Directory -Path $LifecycleDirectory | Out-Null
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $acl = [Security.AccessControl.DirectorySecurity]::new()
        $acl.SetOwner($sid)
        $acl.SetAccessRuleProtection($true,$false)
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid,'FullControl','ContainerInherit,ObjectInherit','None','Allow'))
        Set-Acl -LiteralPath $LifecycleDirectory -AclObject $acl
    }
    Assert-LifecycleDirectory
}
function Read-LifecycleState {
    if (!(Test-Path -LiteralPath $LifecycleDirectory)) { return $null }
    Assert-LifecycleDirectory
    Assert-SafePath $LifecycleFile
    if (!(Test-Path -LiteralPath $LifecycleFile)) { return $null }
    try {
        if ((Get-Item -LiteralPath $LifecycleFile).Length -gt 16384) { throw 'size' }
        $bytes = [Security.Cryptography.ProtectedData]::Unprotect([IO.File]::ReadAllBytes($LifecycleFile),[Text.Encoding]::UTF8.GetBytes($LifecycleRoot),[Security.Cryptography.DataProtectionScope]::CurrentUser)
        $s = [Text.Encoding]::UTF8.GetString($bytes) | ConvertFrom-Json -AsHashtable
        if (($s.Version -isnot [int] -and $s.Version -isnot [long]) -or ($s.Port -isnot [int] -and $s.Port -isnot [long]) -or $s.Instance -isnot [string] -or $s.Phase -isnot [string]) { throw 'types' }
        if (($s.Keys | Sort-Object) -join ',' -ne 'Attempted,Development,Instance,MCP,Phase,Port,Tunnel,Version' -or $s.Version -ne 1 -or $s.Instance -cnotmatch '^[a-f0-9]{32}$' -or $s.Phase -notin 'STOPPED','STARTING','READY','DEGRADED','STOPPING','FAILED' -or $s.Port -lt 1 -or $s.Port -gt 65535) { throw 'schema' }
        if ($s.Attempted -isnot [array] -or $s.Attempted.Count -gt 3 -or @($s.Attempted | Select-Object -Unique).Count -ne $s.Attempted.Count) { throw 'components' }
        foreach ($c in $s.Attempted) { if ($c -notin 'MCP','Development','Tunnel') { throw 'component' } }
        foreach ($c in 'MCP','Development') {
            if ($s[$c] -and ($s[$c].Id -isnot [string] -or $s[$c].Project -isnot [string])) { throw 'container types' }
            if ($s[$c] -and (($s[$c].Keys | Sort-Object) -join ',' -ne 'Id,Project' -or $s[$c].Id -cnotmatch '^[a-f0-9]{64}$' -or $s[$c].Project -cne (Get-LifecycleProject $s $c))) { throw 'container' }
            if ($s[$c] -and $s.Attempted -notcontains $c) { throw 'unattempted container' }
        }
        if ($s.Tunnel -and (($s.Tunnel.Keys | Sort-Object) -join ',' -ne 'Executable,Pid,Started' -or $s.Tunnel.Pid -lt 1 -or $s.Tunnel.Started -notmatch '^\d+$')) { throw 'process' }
        if ($s.Tunnel -and (($s.Tunnel.Pid -isnot [int] -and $s.Tunnel.Pid -isnot [long]) -or $s.Tunnel.Pid -gt [int]::MaxValue -or $s.Tunnel.Started -isnot [string] -or $s.Tunnel.Executable -isnot [string])) { throw 'process types' }
        if ($s.Tunnel) { Assert-SafePath $s.Tunnel.Executable -Leaf }
        if ($s.Tunnel -and $s.Attempted -notcontains 'Tunnel') { throw 'unattempted process' }
        if ($s.Phase -eq 'READY' -and ($s.Attempted.Count -ne 3 -or !$s.MCP -or !$s.Development -or !$s.Tunnel)) { throw 'incomplete ready' }
        return $s
    } catch { throw 'Lifecycle metadata invalid; no ownership granted' }
}
function Write-LifecycleState($State) {
    Assert-LifecycleDirectory
    Assert-SafePath $LifecycleFile
    $plain = [Text.Encoding]::UTF8.GetBytes(($State | ConvertTo-Json -Depth 5 -Compress))
    $bytes = [Security.Cryptography.ProtectedData]::Protect($plain,[Text.Encoding]::UTF8.GetBytes($LifecycleRoot),[Security.Cryptography.DataProtectionScope]::CurrentUser)
    $temp = Join-Path $LifecycleDirectory ([guid]::NewGuid().ToString('N')+'.pending')
    $file = [IO.FileStream]::new($temp,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
    try { $file.Write($bytes); $file.Flush($true) } finally { $file.Dispose() }
    [IO.File]::Move($temp,$LifecycleFile,$true)
}
function Get-LifecycleProject($s,$c) {
    $suffix = if ($c -eq 'MCP') { 'mcp' } else { 'development' }
    return "dev-orchestrator-$($s.Instance)-$suffix"
}
function Get-OwnedContainer($s,$c) {
    if ($s.Attempted -notcontains $c) { return $null }
    $project = Get-LifecycleProject $s $c
    $service = if ($c -eq 'MCP') { 'orchestrator' } else { 'development' }
    $ids = Invoke-Docker @('ps','-aq','--no-trunc','--filter',"label=com.docker.compose.project=$project",'--filter',"label=com.docker.compose.service=$service")
    if (!$ids) { return $null }
    if ($ids -cnotmatch '^[a-f0-9]{64}$') { throw 'Ambiguous lifecycle container ownership' }
    if ($s[$c] -and $s[$c].Id -cne $ids) { return $null }
    $format='{"Id":{{json .Id}},"Config":{"Labels":{{json .Config.Labels}}},"State":{{json .State}}}'
    $data = @(Invoke-Docker @('inspect','--format',$format,$ids) | ConvertFrom-Json)[0]
    if ($data.Id -cne $ids -or $data.Config.Labels.'com.docker.compose.project' -cne $project -or $data.Config.Labels.'com.docker.compose.service' -cne $service) { return $null }
    return $data
}
function Get-OwnedTunnel($s) {
    if (!$s.Tunnel) { return $null }
    try {
        $p = Get-Process -Id $s.Tunnel.Pid -ErrorAction Stop
        if ($p.StartTime.ToUniversalTime().Ticks.ToString() -cne $s.Tunnel.Started -or $p.MainModule.FileName -ine $s.Tunnel.Executable) { return $null }
        return $p
    } catch { return $null }
}
function Test-LifecycleOwned($s,$c) {
    if ($c -eq 'Tunnel') { return $null -ne (Get-OwnedTunnel $s) }
    $container = Get-OwnedContainer $s $c
    return $container -and $container.State.Running
}
function Test-LifecycleHealthy($s,$c) {
    if ($c -eq 'Tunnel') { return $null -ne (Get-OwnedTunnel $s) }
    $container = Get-OwnedContainer $s $c
    if (!$container -or !$container.State.Running -or $container.State.Health.Status -ne 'healthy') { return $false }
    if ($c -eq 'MCP') {
        try { return (Invoke-WebRequest "http://127.0.0.1:$($s.Port)/readyz" -TimeoutSec 3 -MaximumRedirection 0).StatusCode -eq 200 } catch { return $false }
    }
    return $true
}
function Stop-OwnedComponent($s,$c) {
    if ($c -eq 'Tunnel') {
        $p = Get-OwnedTunnel $s
        if (!$p) { return }
        # Request graceful GUI shutdown if available, then bounded owned fallback.
        [void]$p.CloseMainWindow()
        if (!$p.WaitForExit(5000)) {
            $p = Get-OwnedTunnel $s
            if ($p) { $p.Kill(); if (!$p.WaitForExit(5000)) { throw 'Tunnel shutdown timed out' } }
        }
        return
    }
    $container = Get-OwnedContainer $s $c
    if (!$container) { return }
    $ownedId = $container.Id
    if ($container.State.Running) { Invoke-Docker @('stop','--time','35',$ownedId) 45 | Out-Null }
    $container = Get-OwnedContainer $s $c
    if (!$container -or $container.Id -cne $ownedId) { return }
    if ($container.State.Running) { throw 'Owned container did not stop' }
    try { Invoke-Docker @('rm',$ownedId) 45 | Out-Null } catch {
        # Another cleanup may have removed it between inspection and rm.
        $remaining = Get-OwnedContainer $s $c
        if ($remaining -and $remaining.Id -ceq $ownedId) { throw }
    }
}
function Read-LifecycleManifest([string]$Path) {
    Assert-SafePath $Path -Leaf
    if ((Get-Item -LiteralPath $Path).Length -gt 16384) { throw 'Lifecycle manifest too large' }
    $m = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json -AsHashtable
    if (($m.Keys | Sort-Object) -join ',' -ne 'DevelopmentConfig,DevelopmentVolume,DiscordTokenFile,LocalPort,MCPConfig,RuntimeAuthVolume,SecuritySource,TunnelExecutable,Workspace') { throw 'Invalid lifecycle manifest schema' }
    foreach ($key in 'DevelopmentConfig','DiscordTokenFile','MCPConfig','TunnelExecutable') { Assert-SafePath $m[$key] -Leaf }
    foreach ($key in 'DevelopmentConfig','MCPConfig') { if ((Get-Item -LiteralPath $m[$key]).Length -gt 1048576) { throw 'Runtime configuration too large' } }
    $credentialLength=(Get-Item -LiteralPath $m.DiscordTokenFile).Length
    if ($credentialLength -lt 1 -or $credentialLength -gt 4096) { throw 'Discord credential file invalid' }
    foreach ($key in 'Workspace','SecuritySource') { Assert-SafePath $m[$key]; if (!(Test-Path -LiteralPath $m[$key] -PathType Container)) { throw 'Lifecycle directory prerequisite missing' } }
    foreach ($name in 'auth.json','grants.json') { Assert-SafePath (Join-Path $m.SecuritySource $name) -Leaf }
    if ($m.LocalPort -isnot [long] -and $m.LocalPort -isnot [int]) { throw 'Invalid lifecycle port' }
    if ($m.LocalPort -lt 1 -or $m.LocalPort -gt 65535) { throw 'Invalid lifecycle port' }
    foreach ($key in 'DevelopmentVolume','RuntimeAuthVolume') { if ($m[$key] -cnotmatch '^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$') { throw 'Invalid lifecycle volume' } }
    if ([IO.Path]::GetExtension($m.TunnelExecutable) -ine '.exe') { throw 'Tunnel executable required' }
    return $m
}
function Set-DevelopmentEnvironment($m) {
    $env:DEV_CONFIG_FILE=$m.DevelopmentConfig; $env:DEV_DISCORD_TOKEN_FILE=$m.DiscordTokenFile
    $env:DEV_OPERATIONAL_VOLUME=$m.DevelopmentVolume; $env:DEV_RUNTIME_AUTH_VOLUME=$m.RuntimeAuthVolume
}
function Invoke-LifecycleCommand([string]$Command,[string]$Manifest) {
    if (!$IsWindows) { throw 'Operational launcher requires Windows PowerShell 7' }
    if ($Command -eq 'status') {
        $observation=@{Owned={param($s,$c) Test-LifecycleOwned $s $c};Healthy={param($s,$c) Test-LifecycleHealthy $s $c}}
        return Get-LifecycleStatus (Read-LifecycleState) $observation
    }
    $script:ManifestPath=$Manifest
    $script:ManifestData=$null
    $saved=@{}
    foreach ($name in 'DEV_CONFIG_FILE','DEV_DISCORD_TOKEN_FILE','DEV_OPERATIONAL_VOLUME','DEV_RUNTIME_AUTH_VOLUME','MCP_RUNTIME_AUTHORIZATION') { $saved[$name]=[Environment]::GetEnvironmentVariable($name) }
    $adapter=@{
        Lock={
            Initialize-LifecycleDirectory
            Assert-SafePath (Join-Path $LifecycleDirectory 'lifecycle.lock')
            try { [IO.FileStream]::new((Join-Path $LifecycleDirectory 'lifecycle.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None) } catch { throw 'Concurrent lifecycle command rejected' }
        }
        Load={ Read-LifecycleState }
        Save={ param($s) Write-LifecycleState $s }
        New={ @{Version=1;Instance=[guid]::NewGuid().ToString('N');Phase='STOPPED';Port=$ManifestData.LocalPort;Attempted=@();MCP=$null;Development=$null;Tunnel=$null} }
        Owned={ param($s,$c) Test-LifecycleOwned $s $c }
        Healthy={ param($s,$c) Test-LifecycleHealthy $s $c }
        Stop={ param($s,$c) Stop-OwnedComponent $s $c }
        Validate={
            $script:ManifestData=Read-LifecycleManifest $ManifestPath
            try {
                $mcp = Get-Content -Raw -LiteralPath $ManifestData.MCPConfig | ConvertFrom-Json -AsHashtable
                if (!$mcp.DisableDiscord -or !$mcp.MCP) { throw 'config' }
            } catch { throw 'Invalid MCP configuration' }
            foreach ($name in 'CONTROL_PLANE_API_KEY','MCP_CLIENT_TOKEN','CONTROL_PLANE_TUNNEL_ID') { if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name))) { throw "Lifecycle prerequisite missing: $name" } }
            Set-DevelopmentEnvironment $ManifestData
            Get-Command docker -ErrorAction Stop | Out-Null
            if ((Invoke-Docker @('info','--format','{{.OSType}}')) -ne 'linux') { throw 'Linux Docker required' }
            foreach ($volume in $ManifestData.DevelopmentVolume,$ManifestData.RuntimeAuthVolume) { Invoke-Docker @('volume','inspect',$volume) | Out-Null }
            $listener=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,$ManifestData.LocalPort)
            try { $listener.Start() } catch { throw 'Lifecycle MCP port unavailable' } finally { $listener.Stop() }
            $compose=@('compose','-p','dev-orchestrator-development-check','-f',"$LifecycleRoot/deploy/development.compose.yml")
            Invoke-Docker ($compose+@('config','--quiet')) | Out-Null
            Invoke-Docker ($compose+@('build','development')) 600 | Out-Null
            # Existing validator: no gateway, credential read, provider or domain effects.
            Invoke-Docker ($compose+@('run','--rm','--no-deps','development','--operational','--config','/run/config/development.json','--check-config')) 60 | Out-Null
        }
        Start={
            param($s,$c)
            if ($c -eq 'MCP') {
                & "$LifecycleRoot/scripts/mcp-runtime.ps1" -Managed -ManagedProject (Get-LifecycleProject $s $c) -ConfigFile $ManifestData.MCPConfig -Workspace $ManifestData.Workspace -SecuritySource $ManifestData.SecuritySource -TunnelExecutable $ManifestData.TunnelExecutable -LocalPort $s.Port | Out-Null
            } elseif ($c -eq 'Development') {
                Invoke-Docker @('compose','-p',(Get-LifecycleProject $s $c),'-f',"$LifecycleRoot/deploy/development.compose.yml",'up','-d','--no-build','development') 60 | Out-Null
            } else {
                $env:MCP_RUNTIME_AUTHORIZATION='Bearer '+$env:MCP_CLIENT_TOKEN
                $arguments=@('run','--control-plane.api-key','env:CONTROL_PLANE_API_KEY','--health.listen-addr','127.0.0.1:0','--mcp.server-url',"http://127.0.0.1:$($s.Port)/mcp",'--mcp.extra-headers','"Authorization: env:MCP_RUNTIME_AUTHORIZATION"','--mcp.discovery-extra-headers','"Authorization: env:MCP_RUNTIME_AUTHORIZATION"','--mcp.startup-wait-timeout','30s','--log.http-raw-unsafe=false','--log.file','stdout','--log.level','error')
                $p=Start-Process -FilePath $ManifestData.TunnelExecutable -ArgumentList $arguments -WindowStyle Hidden -RedirectStandardOutput 'NUL' -RedirectStandardError '\\.\NUL' -PassThru
                try {
                    $s.Tunnel=@{Pid=$p.Id;Started=$p.StartTime.ToUniversalTime().Ticks.ToString();Executable=$p.MainModule.FileName}
                    Write-LifecycleState $s
                } catch { if (!$p.HasExited) { $p.Kill() }; throw 'Tunnel ownership registration failed' }
                return
            }
            $container=Get-OwnedContainer $s $c
            if (!$container) { throw 'Container ownership registration failed' }
            $s[$c]=@{Id=$container.Id;Project=(Get-LifecycleProject $s $c)}
        }
        Wait={
            param($s,$c)
            if ($c -eq 'Tunnel') {
                for ($i=0;$i -lt 50;$i++) { if (!(Test-LifecycleOwned $s $c)) { throw 'Tunnel exited during startup' }; Start-Sleep -Milliseconds 100 }
                return
            }
            $deadline=[DateTime]::UtcNow.AddSeconds(90)
            do {
                if (Test-LifecycleHealthy $s $c) { return }
                Start-Sleep -Milliseconds 500
            } while ([DateTime]::UtcNow -lt $deadline)
            throw 'Component readiness timed out'
        }
    }
    try {
        switch ($Command) {
            start { Start-Lifecycle $adapter }
            stop { if (!(Test-Path -LiteralPath $LifecycleDirectory)) { 'STOPPED' } else { Stop-Lifecycle $adapter } }
        }
    } finally { foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name,$saved[$name]) } }
}
