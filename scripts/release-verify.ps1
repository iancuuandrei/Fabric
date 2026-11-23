[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$ReleaseDirectory)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}
function Read-ZipText($Archive, [string]$Name) {
    $entry = $Archive.GetEntry($Name)
    if ($null -eq $entry) { throw "archive is missing $Name" }
    $reader = [System.IO.StreamReader]::new($entry.Open(), [System.Text.Encoding]::UTF8, $true)
    try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
}
function Get-ZipEntryHash($Archive, [string]$Name) {
    $entry = $Archive.GetEntry($Name)
    if ($null -eq $entry) { throw "archive is missing $Name" }
    $sha = [System.Security.Cryptography.SHA256]::Create()
    $stream = $entry.Open()
    # BitConverter hex keeps this verifier working on Windows PowerShell 5.1
    # and PowerShell 7 ([Convert]::ToHexString requires .NET 5+).
    try { $bytes = $sha.ComputeHash($stream); return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant() }
    finally { $stream.Dispose(); $sha.Dispose() }
}

$root = [System.IO.Path]::GetFullPath($ReleaseDirectory)
if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw "release directory does not exist: $root" }
$releasePath = Join-Path $root 'release.json'
$sumsPath = Join-Path $root 'SHA256SUMS'
if (-not (Test-Path -LiteralPath $releasePath -PathType Leaf) -or -not (Test-Path -LiteralPath $sumsPath -PathType Leaf)) { throw 'release.json or SHA256SUMS is missing' }
$release = Get-Content -LiteralPath $releasePath -Raw | ConvertFrom-Json
if ($release.schema_version -ne 1 -or $release.product -ne 'Fabric' -or $release.release_qualified -ne $false) { throw 'invalid release manifest' }
if ($release.version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$' -or $release.commit -notmatch '^[0-9a-f]{40}$') { throw 'invalid release identity' }
if ($release.source_date_epoch -isnot [long] -and $release.source_date_epoch -isnot [int]) { throw 'invalid source_date_epoch' }
$supportedPlatforms = @('windows/amd64', 'linux/amd64')
$artifactCount = @($release.artifacts).Count
if ($artifactCount -lt 1 -or $artifactCount -gt 2) { throw 'release must contain one or two explicitly declared platform archives' }
# Optional declared platform list must match the artifacts when present.
$declaredPlatforms = @()
if ($null -ne $release.platforms) { $declaredPlatforms = @($release.platforms | ForEach-Object { [string]$_ }) }

$expected = @{}
$platforms = @{}
$versionSuffix = ([string]$release.version).Substring(1)
foreach ($artifact in $release.artifacts) {
    $name = [string]$artifact.file
    if ($name -notmatch '^fabric_[A-Za-z0-9._-]+_(windows|linux)_amd64\.zip$' -or $artifact.sha256 -notmatch '^[0-9a-f]{64}$') { throw "invalid artifact record: $name" }
    if ($expected.ContainsKey($name)) { throw "duplicate artifact: $name" }
    if ($artifact.bytes -le 0) { throw "invalid artifact size: $name" }
    $platformName = [string]$artifact.platform
    if ($supportedPlatforms -notcontains $platformName) { throw "invalid artifact platform: $name" }
    # Artifact filename must carry the exact release version for its platform.
    $expectedFile = "fabric_${versionSuffix}_$($platformName.Replace('/', '_')).zip"
    if ($name -cne $expectedFile) { throw "artifact version mismatch: $name" }
    $expected[$name] = [string]$artifact.sha256
    $platforms[$platformName] = $true
}
if ($platforms.Count -ne $artifactCount -or $platforms.Count -lt 1 -or $platforms.Count -gt 2) { throw 'release platform set is invalid' }
foreach ($key in $platforms.Keys) { if ($supportedPlatforms -notcontains $key) { throw 'release platform set is invalid' } }
if ($declaredPlatforms.Count -gt 0) {
    if ($declaredPlatforms.Count -ne $artifactCount) { throw 'declared platforms differ from release artifacts' }
    $seenDeclaredPlatforms = @{}
    foreach ($declared in $declaredPlatforms) {
        $asSlash = [string]$declared
        if ($asSlash.Contains('-') -and -not $asSlash.Contains('/')) { $asSlash = $asSlash.Replace('-amd64', '/amd64') }
        if ($seenDeclaredPlatforms.ContainsKey($asSlash)) { throw 'duplicate declared platform' }
        $seenDeclaredPlatforms[$asSlash] = $true
        if (-not $platforms.ContainsKey($asSlash)) { throw "declared platform has no artifact: $declared" }
    }
}
# Toolchain record is validated when present (new builds always emit it).
if ($null -ne $release.toolchain) {
    if ([string]::IsNullOrWhiteSpace([string]$release.toolchain.go) -or
        [string]::IsNullOrWhiteSpace([string]$release.toolchain.cargo) -or
        [string]::IsNullOrWhiteSpace([string]$release.toolchain.rustc)) { throw 'invalid release toolchain record' }
}

$seen = @{}
foreach ($line in Get-Content -LiteralPath $sumsPath) {
    if ($line -notmatch '^([0-9a-f]{64})  ([A-Za-z0-9._-]+\.zip)$') { throw "invalid SHA256SUMS line: $line" }
    if ($seen.ContainsKey($Matches[2])) { throw "duplicate artifact: $($Matches[2])" }
    $seen[$Matches[2]] = $Matches[1]
}
if ($seen.Count -ne $expected.Count) { throw 'SHA256SUMS count differs from release manifest' }
foreach ($artifact in $release.artifacts) {
    $name = [string]$artifact.file
    if (-not $seen.ContainsKey($name) -or $seen[$name] -ne $expected[$name]) { throw "SHA256SUMS differs for $name" }
    $archivePath = Join-Path $root $name
    if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf)) { throw "archive is missing: $name" }
    $file = Get-Item -LiteralPath $archivePath
    if ($file.Length -ne [long]$artifact.bytes -or (Get-Sha256 $archivePath) -ne $expected[$name]) { throw "artifact hash or size mismatch: $name" }
    $zip = [System.IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
        $entries = @{}
        foreach ($entry in $zip.Entries) {
            if ($entry.FullName.StartsWith('/') -or $entry.FullName.Contains('..') -or $entry.FullName.Contains('\\') -or $entries.ContainsKey($entry.FullName)) { throw "unsafe or duplicate archive entry: $($entry.FullName)" }
            $entries[$entry.FullName] = $true
        }
        $manifest = Read-ZipText $zip 'manifest.json' | ConvertFrom-Json
        if ($manifest.schema_version -ne 1 -or $manifest.product -ne 'Fabric' -or $manifest.version -ne $release.version -or $manifest.commit -ne $release.commit -or [long]$manifest.source_date_epoch -ne [long]$release.source_date_epoch -or $manifest.release_qualified -ne $false) { throw "archive identity mismatch: $name" }
        $platform = [string]$artifact.platform
        if ($manifest.platform.os + '/' + $manifest.platform.arch -ne $platform) { throw "archive platform mismatch: $name" }
        $expectedRustTargets = if ($platform -eq 'windows/amd64') { @('x86_64-pc-windows-msvc', 'x86_64-pc-windows-gnu') } else { @('x86_64-unknown-linux-gnu') }
        if ($expectedRustTargets -cnotcontains [string]$manifest.platform.rust_target) { throw "archive Rust target mismatch: $name" }
        if ($null -ne $manifest.toolchain) {
            if ([string]::IsNullOrWhiteSpace([string]$manifest.toolchain.go) -or
                [string]::IsNullOrWhiteSpace([string]$manifest.toolchain.cargo) -or
                [string]::IsNullOrWhiteSpace([string]$manifest.toolchain.rustc)) { throw "invalid archive toolchain record: $name" }
        }
        if (@($manifest.components).Count -ne 2) { throw "archive component set invalid: $name" }
        $allowed = @{'manifest.json' = $true}
        # Exact platform-specific name+path identities, each unique.
        if ($platform -eq 'windows/amd64') {
            $expectedComponents = @{ 'fabric' = 'fabric.exe'; 'fabric-ri' = 'fabric-ri.exe' }
        } else {
            $expectedComponents = @{ 'fabric' = 'fabric'; 'fabric-ri' = 'fabric-ri' }
        }
        $seenNames = @{}
        foreach ($component in $manifest.components) {
            $componentName = [string]$component.name
            $componentPath = [string]$component.path
            if (-not $expectedComponents.ContainsKey($componentName)) { throw "invalid component name in $name" }
            if ($componentPath -cne $expectedComponents[$componentName]) { throw "component path mismatch for ${componentName} in ${name}" }
            if ($seenNames.ContainsKey($componentName)) { throw "duplicate component name in $name" }
            $seenNames[$componentName] = $true
            if ($component.sha256 -notmatch '^[0-9a-f]{64}$') { throw "invalid component record in $name" }
            if ($allowed.ContainsKey($componentPath)) { throw "duplicate component path in $name" }
            $allowed[$componentPath] = $true
            if ((Get-ZipEntryHash $zip $componentPath) -ne $component.sha256) { throw "component hash mismatch: $name/$componentPath" }
        }
        if ($allowed.Count -ne 3 -or $entries.Count -ne 3) { throw "archive has unexpected or missing entries: $name" }
        foreach ($entryName in $entries.Keys) { if (-not $allowed.ContainsKey($entryName)) { throw "unexpected archive entry: $name/$entryName" } }
        if ($platform -eq 'windows/amd64' -and @($manifest.components | Where-Object path -notmatch '\.exe$').Count -gt 0) { throw 'Windows archive has non-EXE components' }
        if ($platform -eq 'linux/amd64' -and @($manifest.components | Where-Object path -match '\.exe$').Count -gt 0) { throw 'Linux archive has EXE components' }
    } finally { $zip.Dispose() }
}
Write-Output "PASS release integrity: $root"
