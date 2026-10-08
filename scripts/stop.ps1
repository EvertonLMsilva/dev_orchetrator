#Requires -Version 7.0
[CmdletBinding()]
param()
. "$PSScriptRoot/lifecycle-runtime.ps1"
try { Invoke-LifecycleCommand stop '' } catch { throw 'Lifecycle shutdown failed; ownership or shutdown could not be verified' }
