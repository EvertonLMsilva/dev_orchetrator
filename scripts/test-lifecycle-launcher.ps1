#Requires -Version 7.0
# Exercise the production adapter and reused MCP launcher, without real processes.
$ErrorActionPreference='Stop'
. "$PSScriptRoot/lifecycle-runtime.ps1"
function Assert($condition,$message) { if (!$condition) { throw $message } }
$temp=Join-Path ([IO.Path]::GetTempPath()) ('p11-launcher-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $temp | Out-Null
foreach ($name in 'auth.json','grants.json','development.json') { Set-Content -LiteralPath (Join-Path $temp $name) -Value '{}' }
Set-Content -LiteralPath (Join-Path $temp 'mcp.json') -Value '{"DisableDiscord":true,"MCP":{ "Enabled":true }}'
Set-Content -LiteralPath (Join-Path $temp 'discord_token') -Value 'fake-only-credential'
$manifest=@{MCPConfig=(Join-Path $temp 'mcp.json');Workspace=$temp;SecuritySource=$temp;LocalPort=18089;TunnelExecutable=(Get-Command pwsh).Source;DevelopmentConfig=(Join-Path $temp 'development.json');DiscordTokenFile=(Join-Path $temp 'discord_token');DevelopmentVolume='test-development';RuntimeAuthVolume='test-auth'}
$manifestFile=Join-Path $temp 'manifest.json'
$manifest | ConvertTo-Json | Set-Content -LiteralPath $manifestFile
$global:p11Fake=@{Containers=@{};Calls=[Collections.Generic.List[string]]::new();Tunnel=$null;Failure=''}
function Start-Job {
    param($ArgumentList,$ScriptBlock)
    $a=@($ArgumentList[0]); $line=$a -join ' '
    $p11Fake.Calls.Add($line)
    $output=''; $code=0
    switch ($a[0]) {
        info { $output='linux' }
        ps {
            $project=($a | Where-Object { $_ -like 'label=com.docker.compose.project=*' }) -replace '^label=com.docker.compose.project=',''
            if ($p11Fake.Containers.ContainsKey($project)) { $output=$p11Fake.Containers[$project].Id }
        }
        inspect {
            Assert ($a -contains '--format') 'inspection must fit bounded output without truncation'
            $container=@($p11Fake.Containers.Values | Where-Object Id -eq $a[-1])
            if ($container.Count -eq 1) { $output=ConvertTo-Json -InputObject $container[0] -Depth 6 -Compress }
        }
        stop {
            foreach ($container in $p11Fake.Containers.Values) { if ($container.Id -eq $a[-1]) { $container.State.Running=$false } }
        }
        compose {
            $project=$a[2]
            if ($a -contains 'up') {
                $service=if ($project.EndsWith('-mcp')) { 'orchestrator' } else { 'development' }
                $container=@{Id=('{0:x64}' -f ($p11Fake.Containers.Count+1));Config=@{Labels=@{'com.docker.compose.project'=$project;'com.docker.compose.service'=$service}};State=@{Running=$true;Health=@{Status='healthy'}}}
                $p11Fake.Containers[$project]=$container
                if ($p11Fake.Failure -eq $service) { $code=1 }
            }
            if ($a -contains 'stop' -and $p11Fake.Containers.ContainsKey($project)) { $p11Fake.Containers[$project].State.Running=$false }
        }
    }
    return @{Code=$code;Output=$output}
}
function Wait-Job { param($Job,$Timeout) $Job }
function Receive-Job { param($Job) $Job }
function Stop-Job { param($Job) }
function Remove-Job { param($Job) }
function Invoke-WebRequest { param($Uri,$TimeoutSec,$MaximumRedirection,[switch]$UseBasicParsing) @{StatusCode=200} }
function Start-Sleep { param($Milliseconds) }
function Start-Process {
    param($FilePath,$ArgumentList,$WindowStyle,$RedirectStandardOutput,$RedirectStandardError,[switch]$PassThru)
    $p11Fake.Calls.Add(($ArgumentList -join ' '))
    Assert ($WindowStyle -eq 'Hidden' -and $RedirectStandardOutput -eq 'NUL' -and $RedirectStandardError -eq '\\.\NUL') 'unsafe tunnel output'
    if ($p11Fake.Failure -eq 'tunnel') { throw 'fake tunnel failure' }
    $p=[pscustomobject]@{Id=99884;StartTime=[DateTime]::UtcNow;MainModule=[pscustomobject]@{FileName=$FilePath};HasExited=$false}
    $p | Add-Member ScriptMethod CloseMainWindow { $this.HasExited=$true; return $true }
    $p | Add-Member ScriptMethod WaitForExit { param($timeout) return $this.HasExited }
    $p | Add-Member ScriptMethod Kill { $this.HasExited=$true }
    $p11Fake.Tunnel=$p
    return $p
}
function Get-Process { param($Id,$ErrorAction) if ($p11Fake.Tunnel -and !$p11Fake.Tunnel.HasExited -and $Id -eq $p11Fake.Tunnel.Id) { return $p11Fake.Tunnel }; throw 'fake process absent' }
$saved=@{}
foreach ($key in 'CONTROL_PLANE_API_KEY','MCP_CLIENT_TOKEN','CONTROL_PLANE_TUNNEL_ID') { $saved[$key]=[Environment]::GetEnvironmentVariable($key); [Environment]::SetEnvironmentVariable($key,'fake-secret-sentinel') }
try {
    foreach ($failure in '','orchestrator','development','tunnel') {
        $script:LifecycleDirectory=Join-Path $temp ('owner-'+[guid]::NewGuid().ToString('N'))
        $script:LifecycleFile=Join-Path $LifecycleDirectory 'owner.bin'
        $p11Fake.Containers.Clear(); $p11Fake.Calls.Clear(); $p11Fake.Tunnel=$null; $p11Fake.Failure=$failure
        $failed=$false
        try { $result=Invoke-LifecycleCommand start $manifestFile } catch { $failed=$true; if (!$failure) { throw } }
        Assert ($failed -eq [bool]$failure) 'wrong startup outcome'
        $state=Read-LifecycleState
        if (!$failure) {
            Assert ($result -eq 'READY' -and $state.Phase -eq 'READY') 'not READY'
            $before=$p11Fake.Calls.Count
            Assert ((Invoke-LifecycleCommand status '') -eq 'READY') 'production status'
            Assert (($p11Fake.Calls | Select-Object -Skip $before) -notmatch 'up|build|run|stop') 'status mutation'
            $before=$p11Fake.Calls.Count
            try { Invoke-LifecycleCommand start $manifestFile; throw 'accepted duplicate' } catch { Assert ($_.Exception.Message -ne 'accepted duplicate') 'duplicate' }
            Assert (($p11Fake.Calls | Select-Object -Skip $before) -notmatch 'up|build|run|stop') 'duplicate process'
            $metadataBefore=[Convert]::ToBase64String([IO.File]::ReadAllBytes($LifecycleFile))
            $development=@($p11Fake.Containers.Values | Where-Object { $_.Config.Labels.'com.docker.compose.service' -eq 'development' })[0]
            $development.State.Running=$false
            Assert ((Invoke-LifecycleCommand status '') -eq 'DEGRADED') 'dead component did not degrade'
            Assert ([Convert]::ToBase64String([IO.File]::ReadAllBytes($LifecycleFile)) -ceq $metadataBefore) 'status wrote metadata'
            $development.State.Running=$true
            Assert ((Invoke-LifecycleCommand stop '') -eq 'STOPPED') 'production stop'
            Assert ((Invoke-LifecycleCommand status '') -eq 'STOPPED') 'stale stopped ownership'
        } else { Assert ($state.Phase -eq 'FAILED') 'missing FAILED' }
        foreach ($container in $p11Fake.Containers.Values) { Assert (!$container.State.Running) 'rollback leaked container' }
        Assert (!$p11Fake.Tunnel -or $p11Fake.Tunnel.HasExited) 'rollback leaked tunnel'
        Assert (($p11Fake.Calls -join ',') -notmatch 'fake-secret-sentinel|volume rm|prune|--volumes') 'secret/data contract'
        $decoded=[Text.Encoding]::UTF8.GetString([Security.Cryptography.ProtectedData]::Unprotect([IO.File]::ReadAllBytes($LifecycleFile),[Text.Encoding]::UTF8.GetBytes($LifecycleRoot),[Security.Cryptography.DataProtectionScope]::CurrentUser))
        Assert ($decoded -notmatch 'fake-secret-sentinel|Authorization|auth.json|grants.json|discord_token') 'secret in metadata'
    }
} finally { foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key,$saved[$key]) } }
Write-Output 'P11.2 production launcher doubles PASS (test evidence retained in temp)'
