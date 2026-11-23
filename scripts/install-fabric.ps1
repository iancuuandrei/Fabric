[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ReleaseDirectory,
    [Parameter(Mandatory = $true)][string]$InstallDirectory
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem

function Test-ReparsePoint([string]$Path) {
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    return (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) -or ($null -ne $item.LinkType)
}

$releaseRoot = [System.IO.Path]::GetFullPath($ReleaseDirectory)
$installRoot = [System.IO.Path]::GetFullPath($InstallDirectory)
if (-not [System.IO.Path]::IsPathRooted($InstallDirectory) -or -not [System.IO.Path]::IsPathRooted($installRoot)) { throw 'InstallDirectory must be an absolute path' }
# Filesystem-root guard: compare normalized paths without trailing separators.
# ("C:\" trimmed is "C:"; without trimming, "C:\" -eq "C:" is false and root slips through.)
$normalizedInstall = $installRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
$pathRoot = [System.IO.Path]::GetPathRoot($installRoot)
if ([string]::IsNullOrEmpty($pathRoot)) { throw 'InstallDirectory must be an absolute path' }
$normalizedRoot = $pathRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
if ($normalizedInstall -eq $normalizedRoot) { throw 'refusing to install into a filesystem root' }
& (Join-Path $PSScriptRoot 'release-verify.ps1') -ReleaseDirectory $releaseRoot
$release = Get-Content -LiteralPath (Join-Path $releaseRoot 'release.json') -Raw | ConvertFrom-Json
$platform = if ($env:OS -eq 'Windows_NT') { 'windows/amd64' } else { throw 'this installer supports Windows only' }
if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne [System.Runtime.InteropServices.Architecture]::X64) { throw 'only amd64 installations are supported' }
$artifact = @($release.artifacts | Where-Object platform -eq $platform)
if ($artifact.Count -ne 1) { throw "release has no unique artifact for $platform" }
# Target must be absent: never merge into an existing directory.
if (Test-Path -LiteralPath $installRoot) { throw "InstallDirectory must not already exist: $installRoot" }
$parent = [System.IO.Path]::GetDirectoryName($installRoot)
if ([string]::IsNullOrEmpty($parent) -or -not (Test-Path -LiteralPath $parent -PathType Container)) { throw "InstallDirectory parent must exist: $parent" }
# Reject symlink/reparse anywhere in the parent chain.
$cursor = $parent
while ($null -ne $cursor) {
    if (-not (Test-Path -LiteralPath $cursor -PathType Container)) { throw "InstallDirectory parent must exist: $cursor" }
    if (Test-ReparsePoint $cursor) { throw "refusing symlink/reparse parent: $cursor" }
    $next = [System.IO.Path]::GetDirectoryName($cursor.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar))
    if ([string]::IsNullOrEmpty($next) -or $next -eq $cursor) { break }
    $cursor = $next
}
$canonicalParent = [System.IO.Path]::GetFullPath($parent)
$canonicalInstallParent = [System.IO.Path]::GetDirectoryName($installRoot)
if ($canonicalInstallParent -ne $canonicalParent) { throw 'refusing unsafe installer target path' }
$stage = Join-Path $canonicalParent ('.fabric-install-' + [Guid]::NewGuid().ToString('N'))
$resolvedStage = [System.IO.Path]::GetFullPath($stage)
$stageParent = [System.IO.Path]::GetDirectoryName($resolvedStage)
if ($stageParent -ne $canonicalParent) { throw 'refusing unsafe installer staging path' }
New-Item -ItemType Directory -Path $resolvedStage | Out-Null
try {
    $zipPath = Join-Path $releaseRoot $artifact[0].file
    $zip = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
    try {
        $manifestEntry = $zip.GetEntry('manifest.json')
        if ($null -eq $manifestEntry) { throw 'archive is missing manifest.json' }
        $reader = [System.IO.StreamReader]::new($manifestEntry.Open(), [System.Text.Encoding]::UTF8, $true)
        try { $manifest = $reader.ReadToEnd() | ConvertFrom-Json } finally { $reader.Dispose() }
        if ($manifest.version -ne $release.version -or $manifest.commit -ne $release.commit -or [long]$manifest.source_date_epoch -ne [long]$release.source_date_epoch -or $manifest.release_qualified -ne $false) { throw 'archive identity mismatch' }
        if ($manifest.platform.os + '/' + $manifest.platform.arch -ne $platform) { throw 'archive platform mismatch' }
        $expectedComponents = @{ 'fabric' = 'fabric.exe'; 'fabric-ri' = 'fabric-ri.exe' }
        if (@($manifest.components).Count -ne 2) { throw 'archive component set is invalid' }
        $seenNames = @{}
        $seenPaths = @{}
        foreach ($component in $manifest.components) {
            $componentName = [string]$component.name
            $componentPath = [string]$component.path
            if (-not $expectedComponents.ContainsKey($componentName)) { throw "invalid component name: $componentName" }
            if ($componentPath -cne $expectedComponents[$componentName]) { throw "component path mismatch for ${componentName}" }
            if ($seenNames.ContainsKey($componentName)) { throw "duplicate component name: $componentName" }
            $seenNames[$componentName] = $true
            if ($seenPaths.ContainsKey($componentPath)) { throw "duplicate component path: $componentPath" }
            $seenPaths[$componentPath] = $true
            if ([string]$component.sha256 -notmatch '^[0-9a-f]{64}$') { throw "invalid component hash: $componentPath" }
            $entry = $zip.GetEntry($componentPath)
            if ($null -eq $entry) { throw "archive is missing $componentPath" }
            $destination = Join-Path $resolvedStage $componentPath
            $archiveStream = $entry.Open()
            $output = [System.IO.File]::Create($destination)
            try { $archiveStream.CopyTo($output) } finally { $output.Dispose(); $archiveStream.Dispose() }
            if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant() -ne $component.sha256) { throw "component hash mismatch: $componentPath" }
        }
    } finally { $zip.Dispose() }
    $stagedFiles = @(Get-ChildItem -LiteralPath $resolvedStage -Force | ForEach-Object { $_.Name })
    if ($stagedFiles.Count -ne 2 -or -not ($stagedFiles -contains 'fabric.exe') -or -not ($stagedFiles -contains 'fabric-ri.exe')) { throw 'staged payload is incomplete' }
    # Atomically rename the whole staging directory to the target; never move
    # individual binaries into place (avoids partial installs on failure).
    Move-Item -LiteralPath $resolvedStage -Destination $installRoot
    $resolvedStage = $null
    Write-Output "PASS installed Fabric $($release.version) to $installRoot"
} finally {
    if (-not [string]::IsNullOrEmpty($resolvedStage)) {
        $check = [System.IO.Path]::GetFullPath($resolvedStage)
        if ([System.IO.Path]::GetDirectoryName($check) -eq $stageParent -and [System.IO.Path]::GetFileName($check).StartsWith('.fabric-install-') -and (Test-Path -LiteralPath $check -PathType Container)) {
            Remove-Item -LiteralPath $check -Recurse -Force
        }
    }
}
