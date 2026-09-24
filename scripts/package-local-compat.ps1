$ErrorActionPreference = 'Stop'

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
    if ([string]::Equals($baseFull.TrimEnd([char[]](92, 47)), $targetFull.TrimEnd([char[]](92, 47)), $comparison)) {
        return '.'
    }
    $basePrefix = $baseFull.TrimEnd([char[]](92, 47)) + [System.IO.Path]::DirectorySeparatorChar
    if ($targetFull.StartsWith($basePrefix, $comparison)) {
        return $targetFull.Substring($basePrefix.Length)
    }
    return '..' + [System.IO.Path]::DirectorySeparatorChar + $targetFull
}
