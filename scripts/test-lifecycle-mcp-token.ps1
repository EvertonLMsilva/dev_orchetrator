#Requires -Version 7.0
$ErrorActionPreference='Stop'
. "$PSScriptRoot/lifecycle-runtime.ps1"
function Assert($ok,$message) { if (!$ok) { throw $message } }
$saved=[Environment]::GetEnvironmentVariable('MCP_CLIENT_TOKEN')
$path=[IO.Path]::GetTempFileName()
$token='test-only-local-secret'
try {
    [Environment]::SetEnvironmentVariable('MCP_CLIENT_TOKEN',[NullString]::Value)
    [IO.File]::WriteAllText($path,"$token`n")
    $output=@(Import-LifecycleMCPToken $path)
    Assert ($env:MCP_CLIENT_TOKEN -ceq $token) 'RED: local source did not materialize missing MCP_CLIENT_TOKEN'
    Assert ($output.Count -eq 0) 'token loader emitted output'
    $env:MCP_CLIENT_TOKEN='explicit-test-override'
    Import-LifecycleMCPToken "$path.absent"
    Assert ($env:MCP_CLIENT_TOKEN -ceq 'explicit-test-override') 'explicit environment override lost'
    foreach ($bad in @('', ' ', "one`ntwo", 'Bearer secret', ('x'*4097))) {
        [IO.File]::WriteAllText($path,$bad)
        [Environment]::SetEnvironmentVariable('MCP_CLIENT_TOKEN',[NullString]::Value)
        $failed=$false
        try { Import-LifecycleMCPToken $path } catch {
            $failed=$true
            Assert ($_.Exception.Data['LifecyclePrerequisite'] -ceq 'MCP_CLIENT_TOKEN') 'missing safe prerequisite classification'
            Assert ($_.Exception.Message -ceq 'Lifecycle MCP credential unavailable or invalid') 'unsafe diagnostic'
            Assert ($_.Exception.ToString() -notmatch 'test-only-local-secret|Bearer secret|one\ntwo') 'source content leaked'
        }
        Assert $failed 'invalid local token accepted'
        Assert ($null -eq [Environment]::GetEnvironmentVariable('MCP_CLIENT_TOKEN')) 'invalid token materialized'
    }
    foreach ($source in @("$path.absent",[IO.Path]::GetDirectoryName($path))) {
        $failed=$false;try { Import-LifecycleMCPToken $source } catch { $failed=$true }
        Assert $failed 'unavailable/non-file source accepted'
    }
    [IO.File]::WriteAllBytes($path,[byte[]]@(255,254,255))
    $failed=$false;try { Import-LifecycleMCPToken $path } catch { $failed=$true }
    Assert $failed 'invalid UTF8 accepted'
    [IO.File]::WriteAllText($path,$token)
    $locked=[IO.File]::Open($path,[IO.FileMode]::Open,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)
    try {
        $failed=$false;try { Import-LifecycleMCPToken $path } catch { $failed=$true }
        Assert $failed 'inaccessible token accepted'
    } finally { $locked.Dispose() }
    foreach ($override in @('', 'invalid override')) {
        [Environment]::SetEnvironmentVariable('MCP_CLIENT_TOKEN',$override)
        $failed=$false;try { Import-LifecycleMCPToken $path } catch { $failed=$true }
        Assert $failed 'invalid explicit override silently replaced'
    }
} finally {
    $restore=if ($null -eq $saved) { [NullString]::Value } else { $saved }
    [Environment]::SetEnvironmentVariable('MCP_CLIENT_TOKEN',$restore)
    Remove-Item -LiteralPath $path
}
Write-Output 'Lifecycle MCP token PASS (synthetic credentials; no runtime start)'
