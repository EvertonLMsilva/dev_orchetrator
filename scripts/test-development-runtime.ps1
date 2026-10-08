$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$ignored=Get-Content -LiteralPath (Join-Path $root '.dockerignore')
foreach ($private in '.runtime','stdout','tunnel-client') { if ($ignored -notcontains $private) { throw 'Local operational files must be excluded from build context' } }
$saved=@{}
foreach ($key in 'DEV_CONFIG_FILE','DEV_DISCORD_TOKEN_FILE','DEV_OPERATIONAL_VOLUME','DEV_RUNTIME_AUTH_VOLUME') { $saved[$key]=[Environment]::GetEnvironmentVariable($key) }
try {
    $env:DEV_CONFIG_FILE=Join-Path $root 'deploy/lifecycle.example.json'
    $env:DEV_DISCORD_TOKEN_FILE=Join-Path $root 'deploy/lifecycle.example.json'
    $env:DEV_OPERATIONAL_VOLUME='test-operational'; $env:DEV_RUNTIME_AUTH_VOLUME='test-auth'
    $raw=& docker compose -f "$root/deploy/development.compose.yml" config --format json
    if ($LASTEXITCODE) { throw 'Development compose config failed' }
    $c=($raw -join "`n") | ConvertFrom-Json
    $s=$c.services.development
    if (!$s.read_only -or !$s.init -or $s.restart -ne 'no' -or $s.cap_drop -notcontains 'ALL' -or $s.security_opt -notcontains 'no-new-privileges:true') { throw 'Development isolation contract' }
    if ($s.command -notcontains '--operational' -or $s.command -notcontains '--ready-listen' -or $s.command -notcontains '127.0.0.1:8081' -or $s.command -contains '--register-commands') { throw 'Development invocation contract' }
    if ($s.PSObject.Properties.Name -contains 'ports') { throw 'Development readiness published' }
    if (!$c.volumes.operational.external -or !$c.volumes.'runtime-auth'.external) { throw 'Persistent external volumes required' }
    $config=$s.volumes | Where-Object target -eq '/run/config/development.json'
    if (!$config.read_only -or $config.bind.create_host_path) { throw 'Configuration mount contract' }
    if ($s.build.target -ne 'development-runtime' -or ($s.healthcheck.test -join ' ') -notmatch '127.0.0.1:8081/readyz') { throw 'Runtime readiness contract' }
    Write-Output 'P11.2 Development Compose contracts PASS'
} finally { foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key,$saved[$key]) } }
