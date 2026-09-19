param()

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$localRoot = [IO.Path]::GetFullPath((Join-Path $root '.local'))
$fixture = Join-Path $localRoot ('package-local-portable-' + [guid]::NewGuid().ToString('N'))

function Write-Utf8([string]$Path, [string]$Content) {
    New-Item -ItemType Directory -Path (Split-Path -Parent $Path) -Force | Out-Null
    [IO.File]::WriteAllText($Path, $Content, [Text.UTF8Encoding]::new($false))
}

function Expect-Failure([string]$Name, [scriptblock]$Body, [string]$Pattern) {
    $failed = $false
    try {
        & $Body > $null 2>&1
    } catch {
        $failed = $true
        $message = $_.Exception.Message
        if ($message -notmatch $Pattern) {
            throw "$Name failed with an unexpected error: $message"
        }
        if ($message -match 'signed release|published release') {
            throw "$Name reported a signed or published release: $message"
        }
        Write-Output "PASS $Name"
    }
    if (-not $failed) { throw "$Name unexpectedly succeeded" }
}

try {
    New-Item -ItemType Directory -Path $fixture -Force | Out-Null

    $packText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'package-local.ps1')
    $verifyText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-local-package.ps1')
    foreach ($text in @($packText, $verifyText)) {
        if ($text -match '\.local/toolchains') { throw 'packaging scripts must not reference .local/toolchains' }
        if ($text -match 'USERPROFILE') { throw 'packaging scripts must not reference USERPROFILE' }
        if ($text -match 'Get-Command') { throw 'packaging scripts must not discover tools from PATH' }
    }
    Write-Output 'PASS no private toolchain fallback'

    $toolDir = Join-Path $fixture 'tools'
    New-Item -ItemType Directory -Path $toolDir -Force | Out-Null
    $fakeGo = Join-Path $toolDir 'go.exe'
    $fakeGit = Join-Path $toolDir 'git.exe'
    $fakeCargo = Join-Path $toolDir 'cargo.exe'
    $fakeRustc = Join-Path $toolDir 'rustc.exe'
    foreach ($tool in @($fakeGo, $fakeGit, $fakeCargo, $fakeRustc)) {
        Write-Utf8 $tool 'fake tool'
    }
    $missingCargo = Join-Path $toolDir 'missing-cargo.exe'
    $packageScript = Join-Path $PSScriptRoot 'package-local.ps1'
    $common = @{
        Git        = $fakeGit
        Cargo      = $fakeCargo
        Rustc      = $fakeRustc
        GoOS       = 'windows'
        GoArch     = 'amd64'
        RustTarget = 'x86_64-pc-windows-msvc'
    }
    Expect-Failure 'reject relative Go path' {
        & $packageScript -OutputDirectory (Join-Path $fixture 'pkg-relative') -BuildDirectory (Join-Path $fixture 'build-relative') -Go 'relative\go.exe' @common
    } 'absolute executable path'
    Expect-Failure 'reject missing Cargo tool' {
        & $packageScript -OutputDirectory (Join-Path $fixture 'pkg-missing') -BuildDirectory (Join-Path $fixture 'build-missing') -Go $fakeGo -Git $fakeGit -Cargo $missingCargo -Rustc $fakeRustc -GoOS 'windows' -GoArch 'amd64' -RustTarget 'x86_64-pc-windows-msvc'
    } 'Cannot find path'
    $existingOutput = Join-Path $fixture 'pkg-exists'
    New-Item -ItemType Directory -Path $existingOutput -Force | Out-Null
    Expect-Failure 'reject pre-existing output directory' {
        & $packageScript -OutputDirectory $existingOutput -BuildDirectory (Join-Path $fixture 'build-exists') -Go $fakeGo @common
    } 'must not already exist'
    $existingBuild = Join-Path $fixture 'build-exists-dir'
    New-Item -ItemType Directory -Path $existingBuild -Force | Out-Null
    Expect-Failure 'reject pre-existing build directory' {
        & $packageScript -OutputDirectory (Join-Path $fixture 'pkg-fresh') -BuildDirectory $existingBuild -Go $fakeGo @common
    } 'must not already exist'
    $same = Join-Path $fixture 'same-dir'
    Expect-Failure 'reject identical output and build directories' {
        & $packageScript -OutputDirectory $same -BuildDirectory $same -Go $fakeGo @common
    } 'must differ'
    Expect-Failure 'reject contained build directory' {
        & $packageScript -OutputDirectory (Join-Path $fixture 'pkg-parent') -BuildDirectory (Join-Path (Join-Path $fixture 'pkg-parent') 'child') -Go $fakeGo @common
    } 'must not contain'
    Write-Output 'PASS portable path negative cases'

    $template = Join-Path $fixture 'template'
    New-Item -ItemType Directory -Path (Join-Path $template 'bin') -Force | Out-Null
    Write-Utf8 (Join-Path $template 'bin/engorch.cmd') "@exit /b 0`n"
    Write-Utf8 (Join-Path $template 'bin/engorch-ri.cmd') "@exit /b 2`n"
    $records = @()
    foreach ($relative in @('bin/engorch.cmd', 'bin/engorch-ri.cmd')) {
        $path = Join-Path $template $relative
        $records += [ordered]@{
            path   = $relative
            bytes  = [int64](Get-Item -LiteralPath $path).Length
            sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    }
    $sumText = (($records | ForEach-Object { "$($_.sha256)  $($_.path)" }) -join "`n") + "`n"
    Write-Utf8 (Join-Path $template 'SHA256SUMS') $sumText
    $sumRecord = [ordered]@{
        path   = 'SHA256SUMS'
        bytes  = [int64](Get-Item -LiteralPath (Join-Path $template 'SHA256SUMS')).Length
        sha256 = (Get-FileHash -LiteralPath (Join-Path $template 'SHA256SUMS') -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $manifest = [ordered]@{
        schema_version    = 1
        artifact_kind     = 'local-development-package'
        release_qualified = $false
        source            = [ordered]@{ mode = 'development-unborn'; head = $null; tree = $null; clean = $false }
        components        = [ordered]@{ go_binary = 'engorch.cmd'; rust_binary = 'engorch-ri.cmd' }
        files             = @($records + $sumRecord)
    }
    Write-Utf8 (Join-Path $template 'manifest.json') (($manifest | ConvertTo-Json -Depth 8) + "`n")
    $verifyScript = Join-Path $PSScriptRoot 'verify-local-package.ps1'
    function Copy-Template([string]$Name) {
        $destination = Join-Path $fixture $Name
        Copy-Item -LiteralPath $template -Destination $destination -Recurse -Force
        return $destination
    }
    $extra = Copy-Template 'package-extra'
    Write-Utf8 (Join-Path $extra 'unexpected.txt') "extra`n"
    Expect-Failure 'verifier rejects extra file' {
        & $verifyScript -PackageDirectory $extra
    } 'unlisted or missing files'
    $tampered = Copy-Template 'package-tampered'
    Write-Utf8 (Join-Path $tampered 'bin/engorch.cmd') "@exit /b 1`n"
    Expect-Failure 'verifier rejects tampered payload' {
        & $verifyScript -PackageDirectory $tampered
    } 'package (size|hash) mismatch'
    $badSums = Copy-Template 'package-badsums'
    Write-Utf8 (Join-Path $badSums 'SHA256SUMS') "not-a-checksum-line`n"
    Expect-Failure 'verifier rejects invalid SHA256SUMS' {
        & $verifyScript -PackageDirectory $badSums
    } 'invalid SHA256SUMS line|SHA256SUMS mismatch|package (size|hash) mismatch'
    $released = Copy-Template 'package-released'
    $releasedManifest = Get-Content -Raw -LiteralPath (Join-Path $released 'manifest.json') | ConvertFrom-Json
    $releasedManifest.release_qualified = $true
    Write-Utf8 (Join-Path $released 'manifest.json') (($releasedManifest | ConvertTo-Json -Depth 8) + "`n")
    Expect-Failure 'verifier rejects release-qualified claim' {
        & $verifyScript -PackageDirectory $released
    } 'version or qualification label is invalid'
    $wrongKind = Copy-Template 'package-wrongkind'
    $wrongManifest = Get-Content -Raw -LiteralPath (Join-Path $wrongKind 'manifest.json') | ConvertFrom-Json
    $wrongManifest.artifact_kind = 'signed-release'
    Write-Utf8 (Join-Path $wrongKind 'manifest.json') (($wrongManifest | ConvertTo-Json -Depth 8) + "`n")
    Expect-Failure 'verifier rejects non-local artifact kind' {
        & $verifyScript -PackageDirectory $wrongKind
    } 'invalid local artifact kind'
    Write-Output 'PASS tampered package is never a signed or published release'
} finally {
    $resolvedFixture = [IO.Path]::GetFullPath($fixture)
    $prefix = $localRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if ($resolvedFixture.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $resolvedFixture)) {
        Remove-Item -LiteralPath $resolvedFixture -Recurse -Force
    }
}
