function Invoke-Docker([string[]]$Arguments, [int]$Timeout = 60) {
    $job = Start-Job -ArgumentList (,$Arguments) -ScriptBlock {
        param($a)
        $output = & docker @a 2>&1 | Select-Object -Last 80
        @{ Code = $LASTEXITCODE; Output = ($output -join "`n") }
    }
    try {
        if (!(Wait-Job $job -Timeout $Timeout)) { throw 'MCP Docker operation timed out' }
        $result = Receive-Job $job
        if ($result.Code -ne 0) {
            $failure=[InvalidOperationException]::new('MCP Docker operation failed (output suppressed)')
            $failure.Data['DockerExitCode']=[int]$result.Code
            throw $failure
        }
        return $result.Output
    } finally { Stop-Job $job; Remove-Job $job }
}
