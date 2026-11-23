[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$')][string]$Version,
    [Parameter(Mandatory = $true)][string]$OutputDirectory,
    [string]$SourceDateEpoch,
    [string]$Go = 'go',
    [string]$Cargo = 'cargo',
    [string]$Rustc = 'rustc',
    [string]$Git = 'git',
    # Explicit platform selector. Default is the native host only: cross-Rust
    # builds need an OS-matched linker this Windows host does not have, so we
    # never silently attempt (and misleadingly half-produce) both bundles.
    [string[]]$Platforms = @()
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Invoke-Checked([string]$Program, [string[]]$Arguments) {
    & $Program @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Program failed with exit code $LASTEXITCODE" }
}

function Resolve-ExistingFile([string]$Path, [string]$Name) {
    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    if ($item.PSIsContainer) { throw "$Name must be a file: $Path" }
    return $item.FullName
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function New-ReproducibleZip([string]$SourceDirectory, [string]$ArchivePath, [DateTimeOffset]$Timestamp) {
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $stream = [System.IO.File]::Open($ArchivePath, [System.IO.FileMode]::CreateNew)
    try {
        $zip = [System.IO.Compression.ZipArchive]::new($stream, [System.IO.Compression.ZipArchiveMode]::Create, $false)
        try {
            $items = @(Get-ChildItem -LiteralPath $SourceDirectory -Recurse -File | ForEach-Object {
                $relative = $_.FullName.Substring($SourceDirectory.TrimEnd([char[]](92, 47)).Length).TrimStart([char[]](92, 47)).Replace('\', '/')
                [PSCustomObject]@{ Relative = $relative; FullName = $_.FullName }
            })
            # Ordinal (byte-wise) sort for determinism across cultures/locales.
            $list = [System.Collections.Generic.List[object]]::new()
            foreach ($item in $items) { $list.Add($item) }
            $list.Sort([System.Comparison[object]]{ param($a, $b) [System.StringComparer]::Ordinal.Compare($a.Relative, $b.Relative) })
            foreach ($item in $list) {
                # NoCompression keeps bytes reproducible across runtimes; Optimal
                # deflate output may vary between runtimes/versions for identical input.
                $entry = $zip.CreateEntry($item.Relative, [System.IO.Compression.CompressionLevel]::NoCompression)
                $entry.LastWriteTime = $Timestamp
                $sourceStream = [System.IO.File]::OpenRead($item.FullName)
                try { $output = $entry.Open(); try { $sourceStream.CopyTo($output) } finally { $output.Dispose() } } finally { $sourceStream.Dispose() }
            }
        } finally { $zip.Dispose() }
    } finally { $stream.Dispose() }
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$out = [System.IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $out) { throw "OutputDirectory must not already exist: $out" }
$rootPrefix = $root.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
$outPrefix = $out.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
if ($out.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase) -or $root.StartsWith($outPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'OutputDirectory must be outside the source tree and must not contain the source tree'
}
$status = (& $Git -C $root status --porcelain=v1 --untracked-files=all | Out-String).Trim()
if ($LASTEXITCODE -ne 0) { throw 'git status failed' }
if ($status) { throw 'a release build requires a clean source tree' }
$commit = (& $Git -C $root rev-parse --verify HEAD | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{40}$') { throw 'release build requires a resolved commit' }
if ([string]::IsNullOrWhiteSpace($SourceDateEpoch)) { $SourceDateEpoch = $env:SOURCE_DATE_EPOCH }
if ([string]::IsNullOrWhiteSpace($SourceDateEpoch)) {
    $SourceDateEpoch = (& $Git -C $root show -s --format=%ct HEAD | Out-String).Trim()
}
if ($SourceDateEpoch -notmatch '^[0-9]+$') { throw 'SourceDateEpoch must be a Unix epoch integer' }
$epoch = [int64]$SourceDateEpoch
$timestamp = [DateTimeOffset]::FromUnixTimeSeconds($epoch)
# ZIP stores timestamps at two-second precision. Normalize once so metadata,
# manifests and archive timestamps all describe the same reproducible instant.
$epoch = [int64]([Math]::Floor($epoch / 2) * 2)
$timestamp = [DateTimeOffset]::FromUnixTimeSeconds($epoch)
if ($timestamp.Year -lt 1980 -or $timestamp.Year -gt 2107) { throw 'SOURCE_DATE_EPOCH must be representable by ZIP timestamps' }

New-Item -ItemType Directory -Path $out | Out-Null
$work = [System.IO.Path]::GetFullPath((Join-Path $out '.work'))
$workPrefix = $outPrefix
if (-not $work.StartsWith($workPrefix, [StringComparison]::OrdinalIgnoreCase) -or
    [System.IO.Path]::GetFileName($work) -ne '.work' -or
    [System.IO.Path]::GetDirectoryName($work).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) -ne $out.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)) {
    throw 'refusing unsafe temporary work directory'
}
New-Item -ItemType Directory -Path $work | Out-Null
$sourceDate = $timestamp.UtcDateTime.ToString('yyyy-MM-ddTHH:mm:ssZ')
$ldflags = "-s -w -X github.com/iancuuandrei/fabric/internal/buildinfo.Version=$Version -X github.com/iancuuandrei/fabric/internal/buildinfo.Commit=$commit -X github.com/iancuuandrei/fabric/internal/buildinfo.Date=$sourceDate"
$supportedPlatforms = @('windows-amd64', 'linux-amd64')
if ($null -eq $Platforms -or $Platforms.Count -eq 0) {
    # Default: native host only. Go reports e.g. "windows/amd64".
    $hostPlatform = ((& $Go env GOOS) | Out-String).Trim() + '/' + (((& $Go env GOARCH) | Out-String).Trim())
    if ($LASTEXITCODE -ne 0) { throw 'go env failed; cannot detect the native platform' }
    $nativePlatformName = $hostPlatform.Replace('/', '-')
    if ($supportedPlatforms -notcontains $nativePlatformName) { throw "native platform $hostPlatform is not a supported release platform (supported: $($supportedPlatforms -join ', '))" }
    $Platforms = @($nativePlatformName)
}
$selectedNames = @()
foreach ($name in $Platforms) {
    if ($supportedPlatforms -notcontains $name) { throw "unsupported platform: $name (supported: $($supportedPlatforms -join ', '))" }
    if ($selectedNames -contains $name) { throw "duplicate platform: $name" }
    $selectedNames += $name
}
$allPlatformSpecs = @(
    [PSCustomObject]@{ Name = 'windows-amd64'; GoOS = 'windows'; GoArch = 'amd64'; RustTarget = 'x86_64-pc-windows-msvc'; Extension = '.exe' },
    [PSCustomObject]@{ Name = 'linux-amd64'; GoOS = 'linux'; GoArch = 'amd64'; RustTarget = 'x86_64-unknown-linux-gnu'; Extension = '' }
)
# NOTE: the filtered collection must NOT be named $platforms/$Platforms:
# PowerShell variables are case-insensitive, so that would alias the
# [string[]]$Platforms parameter and string-coerce the spec objects.
$selectedPlatformSpecs = @($allPlatformSpecs | Where-Object { $selectedNames -contains $_.Name })
if ($selectedPlatformSpecs.Count -ne $selectedNames.Count) { throw 'internal platform selection error' }
# Fail fast when a cross-Rust linker is unavailable instead of producing a
# misleading partial bundle. Go cross-compiles with CGO_ENABLED=0, but the
# Rust target needs an OS-matched linker this host may not have.
$hostIsWindows = ($env:OS -eq 'Windows_NT')
foreach ($platformSpec in $selectedPlatformSpecs) {
    if ($platformSpec.Name -eq 'linux-amd64' -and $hostIsWindows) {
        if (-not (Get-Command 'x86_64-linux-gnu-gcc' -ErrorAction SilentlyContinue)) {
            throw 'cannot build linux-amd64 on this Windows host: no Linux linker (x86_64-linux-gnu-gcc) found; refusing a partial bundle. Re-run with -Platforms windows-amd64 only and build linux-amd64 natively on Linux (see docs/guides/release.md).'
        }
    }
    if ($platformSpec.Name -eq 'windows-amd64' -and -not $hostIsWindows) {
        throw 'cannot build windows-amd64 on this Linux host: Windows MSVC builds require a native Visual Studio toolchain; refusing a partial bundle. Build windows-amd64 natively on Windows.'
    }
}
# Record the exact toolchain used so rebuilds can be compared.
$goVersion = ((& $Go version) | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goVersion)) { throw 'go version failed' }
Push-Location $root
try {
    $cargoVersion = ((& $Cargo --version) | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($cargoVersion)) { throw 'cargo --version failed' }
    $rustcVersion = ((& $Rustc --version) | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($rustcVersion)) { throw 'rustc --version failed' }
} finally { Pop-Location }
$toolchain = [ordered]@{ go = $goVersion; cargo = $cargoVersion; rustc = $rustcVersion }
$artifacts = @()
$savedSourceDateEpoch = $env:SOURCE_DATE_EPOCH
$savedCargoTargetDir = $env:CARGO_TARGET_DIR
$savedReleaseRustc = $env:RUSTC
$env:RUSTC = (Get-Command $Rustc -ErrorAction Stop).Source
$env:SOURCE_DATE_EPOCH = [string]$epoch
$env:CARGO_TARGET_DIR = Join-Path $work 'cargo-target'
try {
foreach ($platformSpec in $selectedPlatformSpecs) {
    $stage = Join-Path $work (Join-Path $platformSpec.Name 'fabric')
    New-Item -ItemType Directory -Path $stage -Force | Out-Null
    $fabric = Join-Path $stage ('fabric' + $platformSpec.Extension)
    $ri = Join-Path $stage ('fabric-ri' + $platformSpec.Extension)
    $savedGoOS, $savedGoArch, $savedCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
    try {
        $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $platformSpec.GoOS, $platformSpec.GoArch, '0'
        Invoke-Checked $Go @('-C', $root, 'build', '-trimpath', '-buildvcs=true', '-ldflags', $ldflags, '-o', $fabric, './cmd/fabric')
    } finally { $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $savedGoOS, $savedGoArch, $savedCGO }
    $savedRustFlags = $env:RUSTFLAGS
    $savedEncodedRustFlags = $env:CARGO_ENCODED_RUSTFLAGS
    try {
        # Use CARGO_ENCODED_RUSTFLAGS with 0x1f separators so checkout paths
        # containing spaces stay a single flag. Preserve prior RUSTFLAGS (left
        # untouched) and prior encoded values (appended after a separator).
        $remap = "--remap-path-prefix=$root=/src"
        $separator = [char]0x1f
        $releaseRustFlags = @($remap, "--remap-path-prefix=$($env:CARGO_TARGET_DIR)=/target")
        if ($platformSpec.RustTarget -eq 'x86_64-pc-windows-msvc') {
            $releaseRustFlags += @('-C', 'target-feature=+crt-static', '-C', 'strip=debuginfo', '-C', 'link-arg=/Brepro', '-C', 'link-arg=/DEBUG:NONE')
        }
        $encodedReleaseRustFlags = $releaseRustFlags -join $separator
        if ([string]::IsNullOrEmpty($savedEncodedRustFlags)) {
            $env:CARGO_ENCODED_RUSTFLAGS = $encodedReleaseRustFlags
        } else {
            $env:CARGO_ENCODED_RUSTFLAGS = $savedEncodedRustFlags + $separator + $encodedReleaseRustFlags
        }
        Push-Location $root
        try { Invoke-Checked $Cargo @('build', '--manifest-path', (Join-Path $root 'Cargo.toml'), '--locked', '--release', '--target', $platformSpec.RustTarget, '--bin', 'fabric-ri') }
        finally { Pop-Location }
    } finally {
        if ([string]::IsNullOrEmpty($savedRustFlags)) {
            Remove-Item Env:\RUSTFLAGS -ErrorAction SilentlyContinue
        } else {
            $env:RUSTFLAGS = $savedRustFlags
        }
        if ([string]::IsNullOrEmpty($savedEncodedRustFlags)) {
            Remove-Item Env:\CARGO_ENCODED_RUSTFLAGS -ErrorAction SilentlyContinue
        } else {
            $env:CARGO_ENCODED_RUSTFLAGS = $savedEncodedRustFlags
        }
    }
    $builtRI = Join-Path $env:CARGO_TARGET_DIR (Join-Path $platformSpec.RustTarget (Join-Path 'release' ('fabric-ri' + $platformSpec.Extension)))
    Copy-Item -LiteralPath (Resolve-ExistingFile $builtRI 'Rust RI binary') -Destination $ri
    $manifest = [ordered]@{
        schema_version = 1; product = 'Fabric'; version = $Version; commit = $commit; source_date_epoch = $epoch; created_utc = $sourceDate
        platform = [ordered]@{ os = $platformSpec.GoOS; arch = $platformSpec.GoArch; rust_target = $platformSpec.RustTarget }
        toolchain = [ordered]@{ go = $goVersion; cargo = $cargoVersion; rustc = $rustcVersion }
        components = @(
            [ordered]@{ name = 'fabric'; path = ('fabric' + $platformSpec.Extension); sha256 = Get-Sha256 $fabric },
            [ordered]@{ name = 'fabric-ri'; path = ('fabric-ri' + $platformSpec.Extension); sha256 = Get-Sha256 $ri }
        )
        release_qualified = $false
        artifact_boundary = 'The archive bundles Fabric and fabric-ri only. It does not include a provider executable, credentials, a signature, a tag, or publication evidence.'
    }
    [System.IO.File]::WriteAllText((Join-Path $stage 'manifest.json'), (($manifest | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    $archiveName = "fabric_$($Version.TrimStart('v'))_$($platformSpec.GoOS)_$($platformSpec.GoArch).zip"
    $archive = Join-Path $out $archiveName
    New-ReproducibleZip $stage $archive $timestamp
    $artifacts += [ordered]@{ file = $archiveName; sha256 = Get-Sha256 $archive; bytes = (Get-Item -LiteralPath $archive).Length; platform = "$($platformSpec.GoOS)/$($platformSpec.GoArch)" }
}
# The build must not have dirtied the tree: recheck HEAD and cleanliness
# before producing the final metadata so the exact clean-commit binding holds.
$postStatus = (& $Git -C $root status --porcelain=v1 --untracked-files=all | Out-String).Trim()
if ($LASTEXITCODE -ne 0) { throw 'git status failed' }
if ($postStatus) { throw 'source tree changed during the release build; refusing to produce release metadata' }
$postCommit = (& $Git -C $root rev-parse --verify HEAD | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $postCommit -ne $commit) { throw 'HEAD changed during the release build; refusing to produce release metadata' }
$sums = @($artifacts | Sort-Object file | ForEach-Object { "$($_.sha256)  $($_.file)" })
[System.IO.File]::WriteAllText((Join-Path $out 'SHA256SUMS'), (($sums -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
$release = [ordered]@{ schema_version = 1; product = 'Fabric'; version = $Version; commit = $commit; source_date_epoch = $epoch; platforms = @($selectedNames); toolchain = [ordered]@{ go = $goVersion; cargo = $cargoVersion; rustc = $rustcVersion }; artifacts = $artifacts; release_qualified = $false; limitations = @('Unsigned local artifacts. No tag, upload, publication, or release acceptance is implied.') }
[System.IO.File]::WriteAllText((Join-Path $out 'release.json'), (($release | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
} finally {
    $env:SOURCE_DATE_EPOCH = $savedSourceDateEpoch
    $env:CARGO_TARGET_DIR = $savedCargoTargetDir
    $env:RUSTC = $savedReleaseRustc
    # Only remove the exact temporary directory created beneath this invocation's
    # fresh output root. Revalidate immediately before recursive deletion.
    $resolvedWork = [System.IO.Path]::GetFullPath($work)
    $resolvedParent = [System.IO.Path]::GetDirectoryName($resolvedWork).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    if ($resolvedParent -eq $out.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) -and
        [System.IO.Path]::GetFileName($resolvedWork) -eq '.work' -and
        (Test-Path -LiteralPath $resolvedWork -PathType Container)) {
        Remove-Item -LiteralPath $resolvedWork -Recurse -Force
    }
}
Write-Output ($release | ConvertTo-Json -Depth 8 -Compress)
