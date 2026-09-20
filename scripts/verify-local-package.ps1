param([Parameter(Mandatory = $true)][string]$PackageDirectory)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Test-PathFullyQualified([string]$Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return $false }
    if (-not [System.IO.Path]::IsPathRooted($Value)) { return $false }
    if ([System.IO.Path]::DirectorySeparatorChar -eq '\') {
        if ($Value -match '^[A-Za-z]:[\\/]') { return $true }
        if ($Value -match '^[\\/]{2}[^\\/]+[\\/][^\\/]+') { return $true }
        return $false
    }
    return $true
}

function Get-RelativePathCustom([string]$Base, [string]$Target) {
    $baseFull = [System.IO.Path]::GetFullPath($Base)
    $targetFull = [System.IO.Path]::GetFullPath($Target)
    $comparison = [System.StringComparison]::OrdinalIgnoreCase
    if ([System.IO.Path]::DirectorySeparatorChar -ne '\') {
        $comparison = [System.StringComparison]::Ordinal
    }
    $baseRoot = [System.IO.Path]::GetPathRoot($baseFull)
    $targetRoot = [System.IO.Path]::GetPathRoot($targetFull)
    if (-not [string]::Equals($baseRoot, $targetRoot, $comparison)) {
        return '..' + [System.IO.Path]::DirectorySeparatorChar + $targetFull
    }
    if ([string]::Equals($baseFull.TrimEnd('\', '/'), $targetFull.TrimEnd('\', '/'), $comparison)) {
        return '.'
    }
    $basePrefix = $baseFull.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    if ($targetFull.StartsWith($basePrefix, $comparison)) {
        return $targetFull.Substring($basePrefix.Length)
    }
    return '..' + [System.IO.Path]::DirectorySeparatorChar + $targetFull
}

if (-not (Test-PathFullyQualified $PackageDirectory)) {
    throw 'PackageDirectory must be absolute'
}
$root = [System.IO.Path]::GetFullPath($PackageDirectory)
if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw 'package directory does not exist' }
$manifestPath = Join-Path $root 'manifest.json'
$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
if ($manifest.schema_version -ne 1 -or $manifest.release_qualified -ne $false) {
    throw 'package manifest version or qualification label is invalid'
}
if ($manifest.artifact_kind -notin @('local-development-package', 'local-commit-package')) {
    throw 'invalid local artifact kind'
}
switch ($manifest.source.mode) {
    'development-unborn' {
        if ($null -ne $manifest.source.head -or $null -ne $manifest.source.tree -or
            $manifest.source.clean -ne $false -or $manifest.artifact_kind -ne 'local-development-package') {
            throw 'unborn source must remain an uncommitted development package'
        }
    }
    'development-dirty' {
        if ($null -eq $manifest.source.head -or $null -eq $manifest.source.tree -or
            $manifest.source.clean -ne $false -or $manifest.artifact_kind -ne 'local-development-package') {
            throw 'dirty source must remain a development package bound to HEAD and tree'
        }
    }
    'commit-clean' {
        if ($null -eq $manifest.source.head -or $null -eq $manifest.source.tree -or
            $manifest.source.clean -ne $true -or $manifest.artifact_kind -ne 'local-commit-package') {
            throw 'clean commit source requires exact commit-package bindings'
        }
    }
    default { throw 'invalid source mode' }
}

$seen = @{}
foreach ($record in $manifest.files) {
    $relative = [string]$record.path
    if ([string]::IsNullOrWhiteSpace($relative) -or (Test-PathFullyQualified $relative) -or
        $relative -match '(^|/)\.\.(/|$)' -or $relative.Contains('\')) {
        throw "invalid manifest path: $relative"
    }
    if ($seen.ContainsKey($relative)) { throw "duplicate manifest path: $relative" }
    $seen[$relative] = $true
    $path = [System.IO.Path]::GetFullPath((Join-Path $root $relative))
    $prefix = $root.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    if (-not $path.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "manifest path escapes package: $relative"
    }
    $item = Get-Item -LiteralPath $path -ErrorAction Stop
    if ($item.PSIsContainer -or $item.LinkType) { throw "package payload must be an ordinary file: $relative" }
    if ([int64]$record.bytes -ne [int64]$item.Length) { throw "package size mismatch: $relative" }
    $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne [string]$record.sha256) { throw "package hash mismatch: $relative" }
}

$actualFiles = @(Get-ChildItem -LiteralPath $root -Recurse -File | ForEach-Object {
    (Get-RelativePathCustom $root $_.FullName).Replace('\', '/')
} | Sort-Object)
$expectedFiles = @($seen.Keys + 'manifest.json' | Sort-Object)
if (($actualFiles -join "`n") -ne ($expectedFiles -join "`n")) { throw 'package contains unlisted or missing files' }

$sumPath = Join-Path $root 'SHA256SUMS'
$sumEntries = @((Get-Content -LiteralPath $sumPath) | Where-Object { $_ -ne '' })
$sumSeen = @{}
foreach ($line in $sumEntries) {
    if ($line -notmatch '^([0-9a-f]{64})  (.+)$') { throw 'invalid SHA256SUMS line' }
    if (-not $seen.ContainsKey($Matches[2]) -or $Matches[2] -eq 'SHA256SUMS') {
        throw "SHA256SUMS references an invalid payload: $($Matches[2])"
    }
    if ($sumSeen.ContainsKey($Matches[2])) { throw "duplicate SHA256SUMS path: $($Matches[2])" }
    $sumSeen[$Matches[2]] = $true
    $actual = (Get-FileHash -LiteralPath (Join-Path $root $Matches[2]) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $Matches[1]) { throw "SHA256SUMS mismatch: $($Matches[2])" }
}
if ($sumEntries.Count -ne $manifest.files.Count - 1) { throw 'SHA256SUMS payload count mismatch' }
foreach ($relative in $seen.Keys) {
    if ($relative -ne 'SHA256SUMS' -and -not $sumSeen.ContainsKey($relative)) {
        throw "SHA256SUMS omits payload: $relative"
    }
}

$goBinary = Join-Path $root (Join-Path 'bin' $manifest.components.go_binary)
$riBinary = Join-Path $root (Join-Path 'bin' $manifest.components.rust_binary)
$help = & $goBinary help 2>&1
if ($LASTEXITCODE -ne 0 -or (($help | Out-String).Trim()).Length -eq 0) { throw 'packaged engorch help smoke failed' }
$riWire = & $riBinary 2>$null
if ($LASTEXITCODE -ne 2) { throw 'packaged engorch-ri usage smoke returned an unexpected status' }
$riEnvelope = (($riWire | Out-String).Trim()) | ConvertFrom-Json
if ($riEnvelope.version -ne 1 -or $riEnvelope.ok -ne $false -or $riEnvelope.error -notmatch '^usage:') {
    throw 'packaged engorch-ri usage smoke returned an unexpected envelope'
}

Write-Output "PASS local package verification: $root"
