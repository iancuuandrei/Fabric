param(
    [Parameter(Mandatory = $true)][string]$RepositoryRoot,
    [Parameter(Mandatory = $true)][string]$Cargo,
    [Parameter(Mandatory = $true)][string]$Go,
    [Parameter(Mandatory = $true)][string]$RustTarget,
    [Parameter(Mandatory = $false)][string]$RustPackage = 'engorch-ri',
    [Parameter(Mandatory = $true)][string]$OutputDirectory
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Resolve-ExistingAbsolutePath([string]$Value, [string]$Name, [bool]$Container) {
    if (-not [IO.Path]::IsPathFullyQualified($Value)) { throw "$Name must be an absolute path" }
    $resolved = [IO.Path]::GetFullPath($Value)
    $item = Get-Item -LiteralPath $resolved -ErrorAction Stop
    if ([bool]$item.PSIsContainer -ne $Container) { throw "$Name has the wrong path type: $resolved" }
    return $item.FullName
}

function Resolve-FreshAbsolutePath([string]$Value, [string]$Name) {
    if (-not [IO.Path]::IsPathFullyQualified($Value)) { throw "$Name must be an absolute path" }
    $resolved = [IO.Path]::GetFullPath($Value)
    if (Test-Path -LiteralPath $resolved) { throw "$Name must not already exist: $resolved" }
    return $resolved
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-SafeSegment([string]$Value) {
    $safe = $Value -replace '[^A-Za-z0-9_.-]', '_'
    if ($safe.Length -gt 80) { $safe = $safe.Substring(0, 80) }
    if ([string]::IsNullOrWhiteSpace($safe)) { return '_' }
    return $safe
}

function Get-StringSha256([string]$Value) {
    $bytes = [Text.Encoding]::UTF8.GetBytes($Value)
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-LockRecords([string]$LockPath) {
    $text = Get-Content -Raw -LiteralPath $LockPath
    $records = @()
    foreach ($match in [regex]::Matches($text, '(?ms)^\[\[package\]\]\r?\n(?<body>.*?)(?=^\[\[package\]\]|\z)')) {
        $body = $match.Groups['body'].Value
        $fields = @{}
        foreach ($name in @('name', 'version', 'source', 'checksum')) {
            $pattern = '(?m)^' + [regex]::Escape($name) + '\s*=\s*"(?<value>[^\"]*)"\s*$'
            $field = [regex]::Match($body, $pattern)
            if ($field.Success) { $fields[$name] = $field.Groups['value'].Value }
        }
        if ($fields.ContainsKey('name') -and $fields.ContainsKey('version')) {
            $records += [pscustomobject]@{
                name = [string]$fields.name
                version = [string]$fields.version
                source = if ($fields.ContainsKey('source')) { [string]$fields.source } else { $null }
                checksum = if ($fields.ContainsKey('checksum')) { [string]$fields.checksum } else { $null }
            }
        }
    }
    return $records
}

function Get-Closure($NodeMap, [string[]]$Seeds, [string[]]$AllowedKinds) {
    $seen = @{}
    $queue = [Collections.Generic.Queue[string]]::new()
    foreach ($seed in $Seeds) {
        if (-not $seen.ContainsKey($seed)) { $seen[$seed] = $true; $queue.Enqueue($seed) }
    }
    while ($queue.Count -gt 0) {
        $id = $queue.Dequeue()
        if (-not $NodeMap.ContainsKey($id)) { throw "metadata resolve node missing: $id" }
        foreach ($dep in @($NodeMap[$id].deps)) {
            $kinds = @($dep.dep_kinds | ForEach-Object { if ($null -eq $_.kind) { 'normal' } else { [string]$_.kind } })
            if (@($kinds | Where-Object { $_ -in $AllowedKinds }).Count -gt 0 -and -not $seen.ContainsKey([string]$dep.pkg)) {
                $seen[[string]$dep.pkg] = $true
                $queue.Enqueue([string]$dep.pkg)
            }
        }
    }
    return $seen
}

function Get-EdgeSeeds($NodeMap, [string[]]$FromIds, [string]$Kind) {
    $seeds = @{}
    foreach ($id in $FromIds) {
        foreach ($dep in @($NodeMap[$id].deps)) {
            $kinds = @($dep.dep_kinds | ForEach-Object { if ($null -eq $_.kind) { 'normal' } else { [string]$_.kind } })
            if ($Kind -in $kinds) { $seeds[[string]$dep.pkg] = $true }
        }
    }
    return @($seeds.Keys)
}

function Get-LicenseCandidates([string]$PackageRoot, $Package, [string]$RepoRoot) {
    $found = @{}
    $items = @()
    if ($null -ne $Package.license_file -and -not [string]::IsNullOrWhiteSpace([string]$Package.license_file)) {
        $candidate = [IO.Path]::GetFullPath((Join-Path $PackageRoot ([string]$Package.license_file)))
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { $found[$candidate] = 'manifest-license-file' }
    }
    foreach ($file in @(Get-ChildItem -LiteralPath $PackageRoot -File -ErrorAction Stop | Sort-Object Name)) {
        if ($file.Name -match '^(?i:LICENSE|COPYING|AUTHORS|NOTICE)(?:$|[-._].*)') {
            if (-not $found.ContainsKey($file.FullName)) { $found[$file.FullName] = 'package-root' }
        }
    }

    $fallbacks = @()
    switch -Regex ([string]$Package.name) {
        '^tgrep-core$' { $fallbacks = @('third_party/tgrep/LICENSE') }
        '^r-efi$' { $fallbacks = @('third_party/dependencies/r-efi/AUTHORS') }
        '^protoc-bin-vendored$' { $fallbacks = @('third_party/dependencies/protoc-bin-vendored/LICENSE.txt') }
        '^protoc-bin-vendored-' { $fallbacks = @() }
    }
    # Assign platform payload evidence without relying on package-cache contents.
    if ([string]$Package.name -match '^protoc-bin-vendored-') {
        $fallbacks = @(
            'third_party/dependencies/protoc-bin-vendored/LICENSE.txt',
            'third_party/dependencies/protobuf-v31.1/LICENSE'
        )
    }
    foreach ($relative in $fallbacks) {
        $candidate = Join-Path $RepoRoot $relative
        if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) { throw "retained evidence missing: $relative" }
        if (-not $found.ContainsKey($candidate)) { $found[$candidate] = "retained:$relative" }
    }
    foreach ($path in @($found.Keys)) {
        $items += [pscustomobject]@{ path = $path; origin = [string]$found[$path] }
    }
    return @($items | Sort-Object origin, @{ Expression = { [IO.Path]::GetFileName($_.path) } }, path)
}

function Copy-Evidence($Candidates, [string]$DestinationRoot, [string]$OutputRoot) {
    $records = @()
    $used = @{}
    foreach ($candidate in @($Candidates)) {
        $base = Get-SafeSegment ([IO.Path]::GetFileName([string]$candidate.path))
        $name = $base
        $ordinal = 2
        while ($used.ContainsKey($name.ToLowerInvariant())) { $name = "$base.$ordinal"; $ordinal++ }
        $used[$name.ToLowerInvariant()] = $true
        $destination = Join-Path $DestinationRoot $name
        $item = Get-Item -LiteralPath $candidate.path -Force -ErrorAction Stop
        if ($item.PSIsContainer -or $item.LinkType) { throw "license evidence must be an ordinary file: $($candidate.path)" }
        Copy-Item -LiteralPath $item.FullName -Destination $destination
        $relative = [IO.Path]::GetRelativePath($OutputRoot, $destination).Replace('\', '/')
        $records += [ordered]@{
            path = $relative
            origin = [string]$candidate.origin
            bytes = [int64]$item.Length
            sha256 = Get-Sha256 $destination
        }
    }
    return @($records | Sort-Object path)
}

$repo = Resolve-ExistingAbsolutePath $RepositoryRoot 'RepositoryRoot' $true
$cargoExe = Resolve-ExistingAbsolutePath $Cargo 'Cargo' $false
$goExe = Resolve-ExistingAbsolutePath $Go 'Go' $false
$output = Resolve-FreshAbsolutePath $OutputDirectory 'OutputDirectory'
if ($RustTarget -notmatch '^[A-Za-z0-9_.-]+$') { throw 'invalid RustTarget' }
if ($RustPackage -notmatch '^[A-Za-z0-9_.-]+$') { throw 'invalid RustPackage' }

$metadataText = & $cargoExe metadata --offline --locked --filter-platform $RustTarget --format-version 1 --manifest-path (Join-Path $repo 'Cargo.toml')
if ($LASTEXITCODE -ne 0) { throw 'offline locked Cargo metadata failed' }
$metadata = (($metadataText | Out-String).Trim()) | ConvertFrom-Json
$workspaceRoot = [IO.Path]::GetFullPath([string]$metadata.workspace_root)
if ($workspaceRoot -ne $repo) { throw 'Cargo workspace root differs from RepositoryRoot' }
$roots = @($metadata.packages | Where-Object { $_.name -eq $RustPackage -and [string]$_.source -eq '' })
if ($roots.Count -ne 1) { throw "expected exactly one workspace package named $RustPackage" }
$rootPackage = $roots[0]

$nodeMap = @{}
foreach ($node in @($metadata.resolve.nodes)) { $nodeMap[[string]$node.id] = $node }
$normalRaw = Get-Closure $nodeMap @([string]$rootPackage.id) @('normal')
$normalIds = @($normalRaw.Keys | Where-Object { $_ -ne [string]$rootPackage.id })
$buildSeeds = Get-EdgeSeeds $nodeMap (@([string]$rootPackage.id) + $normalIds) 'build'
$buildRaw = Get-Closure $nodeMap $buildSeeds @('normal', 'build')
$devSeeds = Get-EdgeSeeds $nodeMap (@([string]$rootPackage.id) + $normalIds) 'dev'
$devRaw = Get-Closure $nodeMap $devSeeds @('normal', 'build', 'dev')

$lockRecords = @(Get-LockRecords (Join-Path $repo 'Cargo.lock'))
if ($lockRecords.Count -eq 0) { throw 'Cargo.lock package records were not parsed' }

$oldProxy, $oldSum = $env:GOPROXY, $env:GOSUMDB
try {
    $env:GOPROXY = 'off'
    $env:GOSUMDB = 'off'
    $goTemplate = '{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{"\t"}}{{.Sum}}{{"\t"}}{{.GoModSum}}{{"\n"}}{{end}}'
    $goLines = @(& $goExe list -m -f $goTemplate all)
    if ($LASTEXITCODE -ne 0) { throw 'offline Go module query failed' }
} finally {
    $env:GOPROXY, $env:GOSUMDB = $oldProxy, $oldSum
}

New-Item -ItemType Directory -Path $output | Out-Null
$rustEvidenceRoot = New-Item -ItemType Directory -Path (Join-Path $output 'evidence/rust') -Force
$goEvidenceRoot = New-Item -ItemType Directory -Path (Join-Path $output 'evidence/go') -Force
$retainedEvidenceRoot = New-Item -ItemType Directory -Path (Join-Path $output 'evidence/retained') -Force

$packageMap = @{}
foreach ($package in @($metadata.packages)) { $packageMap[[string]$package.id] = $package }
$allIds = @($normalIds + @($buildRaw.Keys) + @($devRaw.Keys) | Sort-Object -Unique)
$rustRecords = @()
foreach ($id in $allIds) {
    if (-not $packageMap.ContainsKey($id)) { throw "resolved package metadata missing: $id" }
    $package = $packageMap[$id]
    if ([string]$package.source -eq '') { continue }
    $matches = @($lockRecords | Where-Object {
        $_.name -eq [string]$package.name -and
        $_.version -eq [string]$package.version -and
        $_.source -eq [string]$package.source
    })
    if ($matches.Count -ne 1) { throw "Cargo.lock identity is ambiguous or missing: $($package.name) $($package.version)" }
    $lock = $matches[0]
    if ([string]$package.source -match '^registry\+' -and [string]::IsNullOrWhiteSpace([string]$lock.checksum)) {
        throw "registry dependency has no Cargo.lock checksum: $($package.name) $($package.version)"
    }
    $packageRoot = Split-Path -Parent ([string]$package.manifest_path)
    $candidates = @(Get-LicenseCandidates $packageRoot $package $repo)
    $normal = $normalRaw.ContainsKey($id)
    $build = $buildRaw.ContainsKey($id)
    $dev = $devRaw.ContainsKey($id)
    $evidenceStatus = if ($candidates.Count -gt 0) { 'complete' } else { 'missing' }
    if ($normal -and $evidenceStatus -eq 'missing') {
        throw "normal dependency has no local or retained license evidence: $id"
    }
    $shortId = (Get-StringSha256 $id).Substring(0, 12)
    $directory = Join-Path $rustEvidenceRoot.FullName "$(Get-SafeSegment $package.name)-$(Get-SafeSegment $package.version)-$shortId"
    New-Item -ItemType Directory -Path $directory | Out-Null
    $evidence = if ($candidates.Count -gt 0) { @(Copy-Evidence $candidates $directory $output) } else { @() }
    $closures = @()
    if ($normal) { $closures += 'normal' }
    if ($build) { $closures += 'build' }
    if ($dev) { $closures += 'dev' }
    $rustRecords += [ordered]@{
        id = $id
        name = [string]$package.name
        version = [string]$package.version
        source = [string]$package.source
        checksum = $lock.checksum
        license_expression = [string]$package.license
        closures = $closures
        potentially_linked = $normal
        evidence_status = $evidenceStatus
        evidence = @($evidence)
    }
}

$goRecords = @()
foreach ($line in @($goLines | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
    $parts = @($line -split "`t", 5)
    if ($parts.Count -ne 5) { throw "unexpected Go module record: $line" }
    $modulePath, $version, $directory, $sum, $goModSum = $parts
    if ([string]::IsNullOrWhiteSpace($directory) -or -not (Test-Path -LiteralPath $directory -PathType Container)) {
        throw "offline Go module source is unavailable: $modulePath@$version"
    }
    $pseudo = [pscustomobject]@{ name = $modulePath; license_file = $null }
    $candidates = @(Get-LicenseCandidates $directory $pseudo $repo)
    if ($candidates.Count -eq 0) { throw "Go module has no local license evidence: $modulePath@$version" }
    $shortId = (Get-StringSha256 "$modulePath@$version").Substring(0, 12)
    $destination = Join-Path $goEvidenceRoot.FullName "$(Get-SafeSegment $modulePath)-$(Get-SafeSegment $version)-$shortId"
    New-Item -ItemType Directory -Path $destination | Out-Null
    $goRecords += [ordered]@{
        path = $modulePath
        version = $version
        sum = $sum
        go_mod_sum = $goModSum
        closure = 'resolved-module-graph'
        potentially_linked = $null
        evidence_status = 'complete'
        evidence = @(Copy-Evidence $candidates $destination $output)
    }
}

$retainedFiles = @()
foreach ($relativeRoot in @('third_party/codegraph', 'third_party/graphify', 'third_party/scip', 'third_party/tgrep', 'third_party/opencode', 'third_party/vercel-ai', 'third_party/pi-subagent-tasks')) {
    $sourceRoot = Join-Path $repo $relativeRoot
    foreach ($file in @(Get-ChildItem -LiteralPath $sourceRoot -File | Where-Object { $_.Name -ne 'scip.proto' } | Sort-Object Name)) {
        $relative = [IO.Path]::GetRelativePath($repo, $file.FullName).Replace('\', '/')
        $destination = Join-Path $retainedEvidenceRoot.FullName $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
        Copy-Item -LiteralPath $file.FullName -Destination $destination
        $retainedFiles += [ordered]@{
            source = $relative
            path = [IO.Path]::GetRelativePath($output, $destination).Replace('\', '/')
            bytes = [int64]$file.Length
            sha256 = Get-Sha256 $destination
        }
    }
}
foreach ($relative in @('NOTICE', 'THIRD_PARTY.md')) {
    $file = Get-Item -LiteralPath (Join-Path $repo $relative) -ErrorAction Stop
    $destination = Join-Path $retainedEvidenceRoot.FullName $relative
    Copy-Item -LiteralPath $file.FullName -Destination $destination
    $retainedFiles += [ordered]@{
        source = $relative
        path = [IO.Path]::GetRelativePath($output, $destination).Replace('\', '/')
        bytes = [int64]$file.Length
        sha256 = Get-Sha256 $destination
    }
}

$normalMissing = @($rustRecords | Where-Object { $_.potentially_linked -and $_.evidence_status -ne 'complete' }).Count
$buildMissing = @($rustRecords | Where-Object { 'build' -in $_.closures -and $_.evidence_status -ne 'complete' }).Count
$devMissing = @($rustRecords | Where-Object { 'dev' -in $_.closures -and $_.evidence_status -ne 'complete' }).Count
$goMissing = @($goRecords | Where-Object { $_.evidence_status -ne 'complete' }).Count
$manifest = [ordered]@{
    schema_version = 1
    rust_target = $RustTarget
    rust_package = [ordered]@{ name = [string]$rootPackage.name; version = [string]$rootPackage.version }
    inputs = [ordered]@{
        cargo_lock_sha256 = Get-Sha256 (Join-Path $repo 'Cargo.lock')
        go_mod_sha256 = Get-Sha256 (Join-Path $repo 'go.mod')
        go_sum_sha256 = Get-Sha256 (Join-Path $repo 'go.sum')
    }
    qualification = [ordered]@{
        distributed_dependency_evidence_complete = ($normalMissing -eq 0 -and $goMissing -eq 0)
        build_dependency_evidence_complete = ($buildMissing -eq 0)
        dev_dependency_evidence_complete = ($devMissing -eq 0)
        dependency_evidence_qualified = ($normalMissing -eq 0 -and $goMissing -eq 0)
        package_compliance_qualified = $false
    }
    boundary = 'Rust normal closure identifies target-filtered dependencies that may contribute to the distributed binary; it is not proof that the linker included code from every package. Build and dev closures are reported separately and do not make the distributed payload incomplete when excluded.'
    rust_packages = @($rustRecords | Sort-Object id)
    go_modules = @($goRecords | Sort-Object path, version)
    retained_evidence = @($retainedFiles | Sort-Object source)
}

$manifestPath = Join-Path $output 'dependency-licenses.json'
$utf8 = [Text.UTF8Encoding]::new($false)
[IO.File]::WriteAllText($manifestPath, (($manifest | ConvertTo-Json -Depth 15) + "`n"), $utf8)

$listed = @($manifest.rust_packages.evidence.path) + @($manifest.go_modules.evidence.path) + @($manifest.retained_evidence.path) + 'dependency-licenses.json'
$listed = @($listed | Sort-Object -Unique)
$actual = @(Get-ChildItem -LiteralPath $output -Recurse -File | ForEach-Object { [IO.Path]::GetRelativePath($output, $_.FullName).Replace('\', '/') } | Sort-Object)
if (($listed -join "`n") -ne ($actual -join "`n")) { throw 'collector output contains unlisted or missing files' }

Write-Output ($manifest | ConvertTo-Json -Depth 15 -Compress)
