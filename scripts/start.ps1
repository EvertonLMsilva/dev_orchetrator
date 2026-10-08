#Requires -Version 7.0
[CmdletBinding()]
param([string]$Manifest = (Join-Path (Split-Path $PSScriptRoot -Parent) '.runtime/lifecycle.json'))
. "$PSScriptRoot/lifecycle-runtime.ps1"
try { Invoke-LifecycleCommand start $Manifest } catch { throw 'Lifecycle startup failed (details and provider output suppressed)' }
