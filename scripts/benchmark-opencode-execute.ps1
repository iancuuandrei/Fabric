param(
    [int[]]$Concurrency = @(1, 2, 4),
    [int]$TasksPerLevel = 4,
    [string]$OutputPath = ".local/opencode-execute-benchmark-health-first.json",
    [string]$PythonExecutable = "python"
)

$ErrorActionPreference = "Stop"
if ($TasksPerLevel -lt 1 -or $TasksPerLevel -gt 32) { throw "TasksPerLevel must be between 1 and 32" }
if ($Concurrency.Count -eq 0 -or ($Concurrency | Where-Object { $_ -lt 1 -or $_ -gt 8 })) { throw "Concurrency must contain values between 1 and 8" }

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$go = Join-Path $repositoryRoot ".local/toolchains/go/bin/go.exe"
$openCode = Join-Path $repositoryRoot ".local/toolchains/opencode-1.18.29/opencode.exe"
$testBinary = Join-Path $repositoryRoot (".local/opencode-execute-benchmark-{0}.test.exe" -f $PID)
if (-not (Test-Path -LiteralPath $go -PathType Leaf) -or -not (Test-Path -LiteralPath $openCode -PathType Leaf)) { throw "repository-pinned Go and OpenCode executables are required" }
$openCodeHash = (Get-FileHash -LiteralPath $openCode -Algorithm SHA256).Hash.ToLowerInvariant()
$expectedHash = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
if ($openCodeHash -ne $expectedHash) { throw "pinned OpenCode executable digest differs" }

$destination = [System.IO.Path]::GetFullPath($(if ([System.IO.Path]::IsPathRooted($OutputPath)) { $OutputPath } else { Join-Path $repositoryRoot $OutputPath }))
$rootFull = [System.IO.Path]::GetFullPath($repositoryRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
$insideRepository = $destination.StartsWith($rootFull + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)
if ($destination.Equals($rootFull, [System.StringComparison]::OrdinalIgnoreCase)) { throw "benchmark output path must name a file" }
if ($insideRepository) {
    $relativeOutput = [System.IO.Path]::GetRelativePath($rootFull, $destination).Replace('\', '/')
    & git -C $rootFull check-ignore -q -- $relativeOutput
    if ($LASTEXITCODE -ne 0) { throw "benchmark output inside repository must be ignored so it cannot invalidate source identity" }
}

function Get-DevelopmentSourceIdentity {
    $identityOutput = & $PythonExecutable (Join-Path $repositoryRoot "scripts/benchmark_startup.py") --source-identity-only 2>&1
    if ($LASTEXITCODE -ne 0) { throw "development source identity failed: $($identityOutput -join "`n")" }
    try {
        return (($identityOutput -join "`n") | ConvertFrom-Json)
    } catch {
        throw "development source identity returned invalid JSON"
    }
}

function Get-IdentityJSON($Identity) {
    return ($Identity | ConvertTo-Json -Depth 8 -Compress)
}

$environmentNames = @("ENGORCH_OPENCODE_EXECUTE_BENCHMARK", "ENGORCH_OPENCODE_EXECUTE_CONCURRENCY", "ENGORCH_OPENCODE_EXECUTE_TASKS", "ENGORCH_OPENCODE_EXECUTE_DIAGNOSTIC")
$savedEnvironment = @{}
foreach ($name in $environmentNames) {
    $existing = Get-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
    $savedEnvironment[$name] = if ($null -eq $existing) { $null } else { $existing.Value }
}
$originalLocation = Get-Location
try {
    Set-Location -LiteralPath $repositoryRoot
    $identityBeforeBuild = Get-DevelopmentSourceIdentity
    $identityBeforeJSON = Get-IdentityJSON $identityBeforeBuild
    & $go test -c -o $testBinary ./internal/opencoderuntime
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $testBinary -PathType Leaf)) { throw "OpenCode Execute benchmark test binary build failed" }
    $testBinaryHash = (Get-FileHash -LiteralPath $testBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    $identityAfterBuild = Get-DevelopmentSourceIdentity
    if ((Get-IdentityJSON $identityAfterBuild) -ne $identityBeforeJSON) { throw "development source or Python identity changed during benchmark compilation" }
    Set-Location -LiteralPath (Join-Path $repositoryRoot "internal/opencoderuntime")

    $cancellationOutput = & $testBinary '-test.run=^TestExecuteRejectsCanceledFreshAttemptBeforeRecordingIntent$' '-test.count=1' 2>&1
    $cancellationExit = $LASTEXITCODE
    $levels = @()
    $measuredExecutableHash = $null
    $measuredSourceHash = $null
    foreach ($width in $Concurrency) {
        $env:ENGORCH_OPENCODE_EXECUTE_BENCHMARK = "1"
        $env:ENGORCH_OPENCODE_EXECUTE_CONCURRENCY = [string]$width
        $env:ENGORCH_OPENCODE_EXECUTE_TASKS = [string]$TasksPerLevel
        $env:ENGORCH_OPENCODE_EXECUTE_DIAGNOSTIC = "1"
        $output = & $testBinary '-test.run=^TestPinnedOpenCodeExecuteBenchmarkLevel$' '-test.count=1' '-test.v' 2>&1
        $exitCode = $LASTEXITCODE
        $marker = @($output | Where-Object { $_ -match 'OPENCODE_EXECUTE_BENCHMARK_RESULT (\{.*\})' } | Select-Object -Last 1)
        if ($marker.Count -eq 1 -and $marker[0] -match 'OPENCODE_EXECUTE_BENCHMARK_RESULT (\{.*\})') {
            $level = $Matches[1] | ConvertFrom-Json
            if ($level.executable_sha256 -ne $openCodeHash) { throw "runtime-measured OpenCode digest differs" }
            if ($null -ne $measuredSourceHash -and $measuredSourceHash -ne $level.fixture_source_sha256) { throw "runtime-measured fixture source digest changed between levels" }
            $measuredExecutableHash = $level.executable_sha256
            $measuredSourceHash = $level.fixture_source_sha256
            $samples = @($level.samples)
            $levels += [ordered]@{
                concurrency = $width
                tasks = $TasksPerLevel
                completed = @($samples | Where-Object completed).Count
                errors = @($samples | Where-Object { -not $_.completed }).Count
                elapsed_ms = $level.elapsed_ms
                task_latency_samples_ms = @($samples | ForEach-Object { $_.elapsed_ms })
                samples = $samples
                test_exit_code = $exitCode
            }
        } else {
            $levels += [ordered]@{
                concurrency = $width; tasks = $TasksPerLevel; completed = 0; errors = $TasksPerLevel
                elapsed_ms = $null; task_latency_samples_ms = @(); samples = @(); test_exit_code = $exitCode
                error = ($output -join "`n")
            }
        }
    }

    $report = [ordered]@{
        schema = "engorch.opencode-execute-local-fixture-benchmark.v2"
        generated_at_utc = [DateTimeOffset]::UtcNow.ToString("O")
        test_binary_sha256 = $testBinaryHash
        executable = [ordered]@{ path = $openCode; sha256 = $measuredExecutableHash }
        fixture_source_sha256 = $measuredSourceHash
        development_source = $identityBeforeBuild.source
        source_identity_python = $identityBeforeBuild.python
        source_drift_checks = @("before_compile", "after_compile", "after_matrix")
        scope = "One compiled test binary drives pinned local OpenCode plus production Execute/MCP/proxy/transport against a scripted local TLS provider; no external model-quality or provider-latency claim."
        host_load = "Uncontrolled; samples are raw wall-clock observations from this host. Tiny samples are not summarized as percentiles."
        recovery = "Every completed task validates offline recovery after success with no additional provider request."
        source_limitations = @(
            "Inventory covers Git-tracked and nonignored untracked ordinary files; ignored files, dependencies, toolchains, and external build inputs are outside it.",
            "Development inventory and optional HEAD are not a committed-source or hermetic-build receipt.",
            "Three equality snapshots detect observed drift but cannot exclude a source change that is reverted between snapshots."
        )
        cancellation = [ordered]@{
            scope = "Fresh pre-intent cancellation: no durable intent, process, broker call, or provider request is admitted."
            completed = $cancellationExit -eq 0
            exit_code = $cancellationExit
            error = if ($cancellationExit -eq 0) { $null } else { ($cancellationOutput -join "`n") }
        }
        levels = $levels
    }
    $testBinaryHashAfter = (Get-FileHash -LiteralPath $testBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($testBinaryHashAfter -ne $testBinaryHash) { throw "compiled benchmark test binary changed during matrix" }
    $identityAfterMatrix = Get-DevelopmentSourceIdentity
    if ((Get-IdentityJSON $identityAfterMatrix) -ne $identityBeforeJSON) { throw "development source or Python identity changed during benchmark matrix" }
    $parent = Split-Path -Parent $destination
    if ($parent) { [void](New-Item -ItemType Directory -Force -Path $parent) }
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $destination -Encoding utf8NoBOM
    $report | ConvertTo-Json -Depth 8
    if ($cancellationExit -ne 0 -or @($levels | Where-Object { $_.errors -ne 0 -or $_.test_exit_code -ne 0 }).Count -ne 0) { exit 1 }
}
finally {
    Set-Location -LiteralPath $originalLocation
    Remove-Item -LiteralPath $testBinary -Force -ErrorAction SilentlyContinue
    foreach ($name in $environmentNames) {
        if ($null -eq $savedEnvironment[$name]) { Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue }
        else { Set-Item -LiteralPath "Env:$name" -Value $savedEnvironment[$name] }
    }
}
