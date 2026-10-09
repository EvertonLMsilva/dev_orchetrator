# Pure lifecycle transaction. The adapter owns I/O; status never calls mutations.
Set-StrictMode -Version Latest
function Get-LifecycleStatus($State, $Adapter) {
    if (!$State) { return 'STOPPED' }
    $owned = 0; $healthy = 0
    foreach ($component in 'MCP','Development','Tunnel') {
        if (& $Adapter.Owned $State $component) {
            $owned++
            if (& $Adapter.Healthy $State $component) { $healthy++ }
        }
    }
    if ($owned -eq 0) { return 'STOPPED' }
    if ($State.Phase -in 'STARTING','STOPPING','FAILED') { return $State.Phase }
    if ($owned -eq 3 -and $healthy -eq 3 -and $State.Phase -eq 'READY') { return 'READY' }
    return 'DEGRADED'
}
function Stop-LifecycleComponents($State, $Adapter) {
    $failed = $false
    foreach ($component in 'Tunnel','Development','MCP') {
        try {
            # Docker cleanup must also inspect stopped resources. Its adapter
            # verifies ownership before every mutation; Tunnel keeps its gate.
            if ($component -ne 'Tunnel' -or (& $Adapter.Owned $State $component)) { & $Adapter.Stop $State $component }
        } catch { $failed = $true }
    }
    if ($failed) { throw 'Lifecycle shutdown incomplete' }
}
function Start-Lifecycle($Adapter) {
    $stage='LOCK'; $component='Lifecycle'; $lock=$null
    try {
        $lock = & $Adapter.Lock
        $stage='LOAD'
        $previous = & $Adapter.Load
        $stage='PRECHECK'
        if ($previous -and $previous.Phase -in 'STARTING','STOPPING') { throw 'Interrupted lifecycle requires owned shutdown first' }
        if ((Get-LifecycleStatus $previous $Adapter) -ne 'STOPPED') { throw 'Lifecycle already managed; stop before starting' }
        $stage='VALIDATE'; & $Adapter.Validate
        $stage='NEW'
        $state = & $Adapter.New
        $complete = $false
        try {
            $stage='SAVE'; $state.Phase = 'STARTING'; & $Adapter.Save $state
            foreach ($component in 'MCP','Development','Tunnel') {
                # Durable intent permits inspection of a partially created resource.
                $stage='SAVE'; $state.Attempted += $component; & $Adapter.Save $state
                $stage='START'
                & $Adapter.Start $state $component
                $stage='SAVE'
                & $Adapter.Save $state
                $stage='WAIT'
                & $Adapter.Wait $state $component
            }
            foreach ($component in 'MCP','Development','Tunnel') {
                $stage='AGGREGATE'
                if (!(& $Adapter.Owned $state $component) -or !(& $Adapter.Healthy $state $component)) { throw 'Aggregate readiness failed' }
            }
            $stage='SAVE'; $component='Lifecycle'; $state.Phase = 'READY'; & $Adapter.Save $state
            $complete = $true
            return 'READY'
        } finally {
            if (!$complete) {
                $state.Phase = 'FAILED'
                try { & $Adapter.Save $state } finally { Stop-LifecycleComponents $state $Adapter }
            }
        }
    } catch {
        # No inner exception, provider output, arguments, headers or credentials.
        $failure=[InvalidOperationException]::new("Lifecycle startup failed: stage=$stage component=$component")
        $failure.Data['LifecycleStage']=$stage
        $failure.Data['LifecycleComponent']=$component
        $prerequisite=$_.Exception.Data['LifecyclePrerequisite']
        if ($prerequisite -in 'CONTROL_PLANE_API_KEY','MCP_CLIENT_TOKEN','CONTROL_PLANE_TUNNEL_ID') { $failure.Data['LifecyclePrerequisite']=$prerequisite }
        if ($_.Exception.Data['DockerExitCode'] -is [int]) { $failure.Data['DockerExitCode']=$_.Exception.Data['DockerExitCode'] }
        throw $failure
    } finally { if ($lock) { $lock.Dispose() } }
}
function Stop-Lifecycle($Adapter) {
    $lock = & $Adapter.Lock
    try {
        $state = & $Adapter.Load
        if (!$state) { return 'STOPPED' }
        $state.Phase = 'STOPPING'; & $Adapter.Save $state
        try {
            Stop-LifecycleComponents $state $Adapter
            $state.Phase = 'STOPPED'; & $Adapter.Save $state
            return 'STOPPED'
        } catch {
            $state.Phase = 'FAILED'; & $Adapter.Save $state
            throw 'Lifecycle shutdown incomplete'
        }
    } finally { $lock.Dispose() }
}
