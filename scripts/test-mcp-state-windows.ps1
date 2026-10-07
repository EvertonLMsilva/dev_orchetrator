# Real Windows PowerShell -> Docker Linux; no production volumes or credentials.
param([string]$Runtime = (Join-Path $PSScriptRoot 'mcp-runtime.ps1'))
$ErrorActionPreference = 'Stop'
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($runtime, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Runtime parse failed' }
$invoke = $ast.Find({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Invoke-Docker' }, $true)
Invoke-Expression $invoke.Extent.Text
$state = $ast.Find({ param($n) $n -is [Management.Automation.Language.PipelineAst] -and $n.Extent.Text.StartsWith('Invoke-Docker ') -and $n.Extent.Text.Contains('target=/state') }, $true)
if (!$state) { throw 'Operational state lifecycle command missing' }
$runtimeScripts = Split-Path $runtime -Parent
# Invoke-Expression has no script file context for the automatic PSScriptRoot variable.
$stateCommand = $state.Extent.Text.Replace('$PSScriptRoot', '$runtimeScripts')
$temp = Join-Path ([IO.Path]::GetTempPath()) ('mcp7-state-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp | Out-Null
# Change only the mount at the job boundary; use the official command and job handling.
function Start-Job {
    param($ArgumentList, $ScriptBlock)
    $arguments = [string[]]$ArgumentList[0].Clone()
    for ($i = 0; $i -lt $arguments.Length; $i++) {
        if ($arguments[$i] -like 'type=volume,*target=/state') {
            $arguments[$i] = "type=volume,source=$script:caseVolume,target=/state"
        }
    }
    Microsoft.PowerShell.Core\Start-Job -ArgumentList (,$arguments) -ScriptBlock $ScriptBlock
}
$fixture = Join-Path $temp 'fixture.sh'
$fixtureText = @'
#!/bin/sh
set -eu
case "$1" in
  absent) ;;
  valid) mkdir -m 700 /state/state ;;
  mode) mkdir -m 755 /state/state ;;
  owner) mkdir -m 700 /state/state; chown 1234 /state/state ;;
  file) touch /state/state ;;
  symlink) mkdir -m 700 /state/real; ln -s real /state/state ;;
  dangling) ln -s missing /state/state ;;
esac
'@
[IO.File]::WriteAllText($fixture, $fixtureText.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
foreach ($case in 'absent', 'valid', 'mode', 'owner', 'file', 'symlink', 'dangling') {
    $script:caseVolume = 'mcp7-test-state-' + [guid]::NewGuid().ToString('N')
    Invoke-Docker @('volume','create',$script:caseVolume) | Out-Null
    Invoke-Docker @('run','--rm','--network','none','--mount',"type=volume,source=$script:caseVolume,target=/state",
        '--mount',"type=bind,source=$fixture,target=/fixture.sh,readonly",'--entrypoint','sh',
        'dev-orchestrator-mcp-read-only','/fixture.sh',$case) | Out-Null
    $expected = $case -in 'absent', 'valid'
    $passed = $true
    try { Invoke-Expression $stateCommand } catch {
        if ($_.Exception.Message -ne 'MCP Docker operation failed (output suppressed)') { throw }
        $passed = $false
    }
    if ($passed -ne $expected) { throw "State lifecycle regression: $case expected success=$expected" }
    if ($passed) { Invoke-Expression $stateCommand }
    Write-Output "Real Windows Docker state case PASS: $case"
}
Write-Output 'MCP7 Windows Docker state lifecycle PASS (isolated test volumes retained)'
