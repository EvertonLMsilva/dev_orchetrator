#Requires -Version 7.0
[CmdletBinding()]
param([string]$Manifest = (Join-Path (Split-Path $PSScriptRoot -Parent) '.runtime/lifecycle.json'))
. "$PSScriptRoot/lifecycle-runtime.ps1"
try { Invoke-LifecycleCommand start $Manifest } catch {
    $stage=$_.Exception.Data['LifecycleStage']; $component=$_.Exception.Data['LifecycleComponent']
    if ($stage -in 'LOCK','LOAD','PRECHECK','VALIDATE','NEW','SAVE','START','WAIT','AGGREGATE' -and $component -in 'Lifecycle','MCP','Development','Tunnel') {
        $message="Lifecycle startup failed: stage=$stage component=$component"
        $prerequisite=$_.Exception.Data['LifecyclePrerequisite']
        if ($prerequisite -in 'CONTROL_PLANE_API_KEY','MCP_CLIENT_TOKEN','CONTROL_PLANE_TUNNEL_ID') { $message+=" prerequisite=$prerequisite" }
        if ($_.Exception.Data['DockerExitCode'] -is [int]) { $message+=" dockerExitCode=$($_.Exception.Data['DockerExitCode'])" }
        throw $message
    }
    throw 'Lifecycle startup failed (details and provider output suppressed)'
}
