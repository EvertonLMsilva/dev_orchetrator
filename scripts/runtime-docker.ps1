function Invoke-Docker([string[]]$Arguments, [int]$Timeout = 60) {
    $job = Start-Job -ArgumentList (,$Arguments) -ScriptBlock {
        param($a)
        $output = & docker @a 2>&1 | Select-Object -Last 80
        @{ Code = $LASTEXITCODE; Output = ($output -join "`n") }
    }
    try {
        if (!(Wait-Job $job -Timeout $Timeout)) { throw 'MCP Docker operation timed out' }
        $result = Receive-Job $job
        if ($result.Code -ne 0) { throw 'MCP Docker operation failed (output suppressed)' }
        return $result.Output
    } finally { Stop-Job $job; Remove-Job $job }
}
