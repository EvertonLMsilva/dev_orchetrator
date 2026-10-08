#Requires -Version 7.0
[CmdletBinding()]
param()
. "$PSScriptRoot/lifecycle-runtime.ps1"
try { Invoke-LifecycleCommand status '' } catch { Write-Output 'FAILED'; throw 'Lifecycle observation failed; no effects performed' }
