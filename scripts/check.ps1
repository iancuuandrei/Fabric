param([string]$Go = 'go')
$ErrorActionPreference = 'Stop'

function Invoke-Check([string]$Program, [string[]]$Arguments) {
    & $Program @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Program $Arguments failed: $LASTEXITCODE" }
}

Invoke-Check $Go @('test', '-count=1', './...')
Invoke-Check $Go @('vet', './...')
Invoke-Check $Go @('test', '-race', '-count=1', './...')
Invoke-Check $Go @('mod', 'verify')
$formatting = & $Go fmt ./...
if ($LASTEXITCODE -ne 0) { throw 'Go formatting command failed' }
if ($formatting) { throw "Go files needed formatting: $formatting" }

if (Test-Path -LiteralPath Cargo.toml) {
    Invoke-Check 'cargo' @('fmt', '--all', '--', '--check')
    Invoke-Check 'cargo' @('clippy', '--workspace', '--all-targets', '--', '-D', 'warnings')
    Invoke-Check 'cargo' @('test', '--workspace')
    Invoke-Check 'cargo' @('build', '--bin', 'engorch-ri')
    $riMetadataText = & cargo metadata --format-version 1 --no-deps
    if ($LASTEXITCODE -ne 0) { throw 'Cargo metadata failed' }
    $riMetadata = $riMetadataText | ConvertFrom-Json
    $riExecutableName = if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)) { 'engorch-ri.exe' } else { 'engorch-ri' }
    $riPreviousBinary = $env:ENGORCH_RI_BINARY
    try {
        $env:ENGORCH_RI_BINARY = Join-Path $riMetadata.target_directory "debug/$riExecutableName"
        Invoke-Check $Go @('test', './internal/ri', '-count=1')
        Invoke-Check $Go @('test', './internal/codexruntime', '-run', 'TestActualRIBrokerStatus', '-count=1')
        Invoke-Check $Go @('test', './internal/codexruntime', '-run', 'TestActualLexicalBroker', '-count=1')
        Invoke-Check $Go @('test', './internal/control', '-run', 'TestRIImportActualRustController', '-count=1')
        Invoke-Check $Go @('test', './internal/control', '-run', 'TestWriterLexicalInvocationAndLeasedSelectionActualRust', '-count=1')
        Invoke-Check $Go @('test', './internal/cli', '-run', 'TestRICommandUsesCommittedRepositoryBinding', '-count=1')
        Invoke-Check $Go @('test', './internal/cli', '-run', 'TestRILifecycleActualRustCLI', '-count=1')
        Invoke-Check $Go @('test', './internal/cli', '-run', 'TestLexicalLifecycleActualRustCLI', '-count=1')
    } finally {
        if ($null -eq $riPreviousBinary) { Remove-Item Env:ENGORCH_RI_BINARY -ErrorAction SilentlyContinue }
        else { $env:ENGORCH_RI_BINARY = $riPreviousBinary }
    }
} else {
    Write-Output 'Rust checks: NOT_RUN (RI implementation follows the Go kernel milestone)'
}
