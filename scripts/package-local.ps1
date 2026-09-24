param(
    [Parameter(Mandatory = $true)][string]$OutputDirectory,
    [Parameter(Mandatory = $true)][string]$BuildDirectory,
    [Parameter(Mandatory = $true)][string]$Go,
    [Parameter(Mandatory = $true)][string]$Git,
    [Parameter(Mandatory = $true)][string]$Cargo,
    [Parameter(Mandatory = $true)][string]$Rustc,
    [Parameter(Mandatory = $true)][string]$GoOS,
    [Parameter(Mandatory = $true)][string]$GoArch,
    [Parameter(Mandatory = $true)][string]$RustTarget
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

. (Join-Path $PSScriptRoot 'package-local-compat.ps1')

function Resolve-FreshAbsolutePath([string]$Value, [string]$Name) {
    if (-not (Test-PathFullyQualified $Value)) {
        throw "$Name must be an absolute path"
    }
    $resolved = [System.IO.Path]::GetFullPath($Value)
    if (Test-Path -LiteralPath $resolved) {
        throw "$Name must not already exist: $resolved"
    }
    return $resolved
}

function Resolve-Tool([string]$Value, [string]$Name) {
    if (-not (Test-PathFullyQualified $Value)) {
        throw "$Name must be an absolute executable path"
    }
    $resolved = [System.IO.Path]::GetFullPath($Value)
    $item = Get-Item -LiteralPath $resolved -ErrorAction Stop
    if (-not $item.PSIsContainer) { return $item.FullName }
    throw "$Name must name an executable file"
}

function Invoke-Checked([string]$Program, [string[]]$Arguments) {
    & $Program @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Program failed with exit code $LASTEXITCODE"
    }
}

function Invoke-GoPackageBuild([string]$SelectedGo, [string]$SourceRoot, [string]$OutputGo, [string]$Windows, [string]$Amd64) {
    $oldGoOS, $oldGoArch, $oldCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
    try {
        $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $Windows, $Amd64, '0'
        Invoke-Checked $SelectedGo @('-C', $SourceRoot, 'build', '-trimpath', '-buildvcs=false', '-o', $OutputGo, './cmd/harness')
    } finally {
        $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $oldGoOS, $oldGoArch, $oldCGO
    }
}

function Invoke-RustPackageBuild([string]$SelectedCargo, [string]$SelectedRustc, [string]$SourceRoot, [string]$TargetDir, [string]$TestTarget) {
    $manifestPath = Join-Path $SourceRoot 'Cargo.toml'
    $oldRustc, $oldWrapper, $oldWsWrapper = $env:RUSTC, $env:RUSTC_WRAPPER, $env:RUSTC_WORKSPACE_WRAPPER
    try {
        $env:RUSTC = $SelectedRustc
        $env:RUSTC_WRAPPER = $null
        $env:RUSTC_WORKSPACE_WRAPPER = $null
        Invoke-Checked $SelectedCargo @('build', '--locked', '--release', '--manifest-path', $manifestPath, '--config', "build.rustc-wrapper=''", '--config', "build.rustc-workspace-wrapper=''", '--target-dir', $TargetDir, '--target', $TestTarget, '--bin', 'engorch-ri')
    } finally {
        $env:RUSTC, $env:RUSTC_WRAPPER, $env:RUSTC_WORKSPACE_WRAPPER = $oldRustc, $oldWrapper, $oldWsWrapper
    }
}

function Get-ToolVersion([string]$Program, [string[]]$Arguments) {
    $output = & $Program @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "failed to inspect tool: $Program" }
    return (($output | Out-String).Trim())
}

function Add-HashBytes($Stream, [byte[]]$Bytes) {
    $Stream.Write($Bytes, 0, $Bytes.Length)
}

function Get-SourceIdentity([string]$RepositoryRoot, [string]$GitExecutable) {
    $listed = & $GitExecutable -C $RepositoryRoot ls-files -z --cached --others --exclude-standard
    if ($LASTEXITCODE -ne 0) { throw 'git source inventory failed' }
    $paths = @($listed -split "`0" | Where-Object { $_ -ne '' } | Sort-Object -Unique)
    $stream = New-Object System.IO.MemoryStream
    try {
        Add-HashBytes $stream ([System.Text.Encoding]::UTF8.GetBytes("engorch.development-source.v1`n"))
        foreach ($relative in $paths) {
            if ($relative.Contains("`n") -or $relative.Contains("`r")) {
                throw 'source inventory rejects newline-containing paths'
            }
            $path = Join-Path $RepositoryRoot $relative
            $item = Get-Item -LiteralPath $path -Force -ErrorAction Stop
            if ($item.PSIsContainer -or $item.LinkType) {
                throw "source inventory requires ordinary files: $relative"
            }
            $relativeBytes = [System.Text.Encoding]::UTF8.GetBytes($relative.Replace('\', '/'))
            $length = [System.BitConverter]::GetBytes([System.Net.IPAddress]::HostToNetworkOrder([int64]$relativeBytes.Length))
            Add-HashBytes $stream $length
            Add-HashBytes $stream $relativeBytes
            $sha = [System.Security.Cryptography.SHA256]::Create()
            try {
                $contentDigest = $sha.ComputeHash([System.IO.File]::ReadAllBytes($path))
            } finally {
                $sha.Dispose()
            }
            Add-HashBytes $stream $contentDigest
        }
        $outer = [System.Security.Cryptography.SHA256]::Create()
        try {
            $digestBytes = $outer.ComputeHash($stream.ToArray())
        } finally {
            $outer.Dispose()
        }
        $digest = (($digestBytes | ForEach-Object { $_.ToString('x2') }) -join '')
    } finally {
        $stream.Dispose()
    }
    & $GitExecutable -C $RepositoryRoot rev-parse --quiet --verify HEAD *> $null
    $hasHead = $LASTEXITCODE -eq 0
    $head = $null
    $tree = $null
    if ($hasHead) {
        $head = ((& $GitExecutable -C $RepositoryRoot rev-parse HEAD) | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw 'failed to resolve HEAD' }
        $tree = ((& $GitExecutable -C $RepositoryRoot rev-parse 'HEAD^{tree}') | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw 'failed to resolve HEAD tree' }
    }
    $status = ((& $GitExecutable -C $RepositoryRoot status --porcelain=v1 --untracked-files=all) | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'failed to inspect repository state' }
    $clean = $hasHead -and $status.Length -eq 0
    $mode = if (-not $hasHead) { 'development-unborn' } elseif ($clean) { 'commit-clean' } else { 'development-dirty' }
    return [ordered]@{
        mode = $mode
        head = $head
        tree = $tree
        clean = $clean
        inventory_algorithm = 'engorch.development-source.v1'
        inventory_sha256 = $digest
        inventory_file_count = $paths.Count
    }
}

function Get-FileRecord([string]$PackageRoot, [string]$RelativePath) {
    $normalized = $RelativePath.Replace('\', '/')
    $path = Join-Path $PackageRoot $RelativePath
    $item = Get-Item -LiteralPath $path -ErrorAction Stop
    return [ordered]@{
        path = $normalized
        bytes = [int64]$item.Length
        sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    }
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$output = Resolve-FreshAbsolutePath $OutputDirectory 'OutputDirectory'
$build = Resolve-FreshAbsolutePath $BuildDirectory 'BuildDirectory'
if ($output -eq $build) { throw 'output and build directories must differ' }
$outputPrefix = $output.TrimEnd([char[]](92, 47)) + [System.IO.Path]::DirectorySeparatorChar
$buildPrefix = $build.TrimEnd([char[]](92, 47)) + [System.IO.Path]::DirectorySeparatorChar
if ($output.StartsWith($buildPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
    $build.StartsWith($outputPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'output and build directories must not contain one another'
}
$goExe = Resolve-Tool $Go 'Go'
$gitExe = Resolve-Tool $Git 'Git'
$cargoExe = Resolve-Tool $Cargo 'Cargo'
$rustcExe = Resolve-Tool $Rustc 'Rustc'
foreach ($value in @($GoOS, $GoArch, $RustTarget)) {
    if ($value -notmatch '^[A-Za-z0-9_.-]+$') { throw "invalid build target: $value" }
}
foreach ($candidate in @($output, $build)) {
    $relative = Get-RelativePathCustom $root $candidate
    if (-not $relative.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar) -and $relative -ne '..') {
        & $gitExe -C $root check-ignore -q -- $relative
        if ($LASTEXITCODE -ne 0) { throw "repository-local package paths must be ignored: $candidate" }
    }
}
$sourceBefore = Get-SourceIdentity $root $gitExe
New-Item -ItemType Directory -Path $build | Out-Null
New-Item -ItemType Directory -Path $output | Out-Null
$bin = New-Item -ItemType Directory -Path (Join-Path $output 'bin')
$cargoTarget = New-Item -ItemType Directory -Path (Join-Path $build 'cargo-target')
$metadataText = & $cargoExe metadata --manifest-path (Join-Path $root 'Cargo.toml') --format-version 1 --no-deps
if ($LASTEXITCODE -ne 0) { throw 'cargo metadata failed' }
$metadata = $metadataText | ConvertFrom-Json
$workspaceRoot = [System.IO.Path]::GetFullPath([string]$metadata.workspace_root)
if ($workspaceRoot -ne $root) { throw 'Cargo workspace root differs from package source root' }
$riPackage = @($metadata.packages | Where-Object { $_.targets.name -contains 'engorch-ri' })
if ($riPackage.Count -ne 1) { throw 'Cargo metadata must identify exactly one engorch-ri package' }
$goName = if ($GoOS -eq 'windows') { 'engorch.exe' } else { 'engorch' }
$riName = if ($RustTarget -match 'windows') { 'engorch-ri.exe' } else { 'engorch-ri' }
$goOutput = Join-Path $bin.FullName $goName
$riOutput = Join-Path $bin.FullName $riName
Invoke-GoPackageBuild $goExe $root $goOutput $GoOS $GoArch
Invoke-RustPackageBuild $cargoExe $rustcExe $root $cargoTarget.FullName $RustTarget
$builtRI = Join-Path $cargoTarget.FullName (Join-Path $RustTarget (Join-Path 'release' $riName))
if (-not (Test-Path -LiteralPath $builtRI -PathType Leaf)) { throw "Rust binary missing: $builtRI" }
Copy-Item -LiteralPath $builtRI -Destination $riOutput
$payload = @(
    'LICENSE', 'NOTICE', 'THIRD_PARTY.md', 'docs/guides/local-packaging.md',
    'docs/provenance/go-module-license-inventory.md',
    'integrations/codex/engorch/.codex-plugin/plugin.json',
    'integrations/codex/engorch/README.md',
    'integrations/codex/engorch/skills/engorch/SKILL.md'
)
$thirdParty = @(
    'third_party/pi-subagent-tasks/LICENSE',
    'third_party/pi-subagent-tasks/README.md',
    'third_party/codegraph/LICENSE',
    'third_party/codegraph/README.md',
    'third_party/graphify/LICENSE',
    'third_party/graphify/LICENSE-MIT',
    'third_party/graphify/NOTICE',
    'third_party/graphify/README.md',
    'third_party/scip/LICENSE',
    'third_party/scip/README.md',
    'third_party/tgrep/LICENSE',
    'third_party/opencode/LICENSE',
    'third_party/opencode/README.md',
    'third_party/vercel-ai/LICENSE',
    'third_party/vercel-ai/LICENSE-APACHE-2.0',
    'third_party/vercel-ai/README.md',
    'third_party/dependencies/go-toml-v2/LICENSE',
    'third_party/dependencies/go-toml-v2/README.md',
    'third_party/dependencies/go-github-v89/LICENSE',
    'third_party/dependencies/go-github-v89/AUTHORS',
    'third_party/dependencies/go-github-v89/README.md',
    'third_party/dependencies/jsonschema-v6/LICENSE',
    'third_party/dependencies/jsonschema-v6/README.md',
    'third_party/dependencies/modernc-sqlite/LICENSE',
    'third_party/dependencies/modernc-sqlite/AUTHORS',
    'third_party/dependencies/modernc-sqlite/LICENSE-SQLITE',
    'third_party/dependencies/modernc-sqlite/LICENSE-SQLITE_VEC',
    'third_party/dependencies/modernc-sqlite/README.md',
    'third_party/dependencies/protoc-bin-vendored/LICENSE.txt',
    'third_party/dependencies/protoc-bin-vendored/README.md',
    'third_party/dependencies/protobuf-v31.1/LICENSE',
    'third_party/dependencies/protobuf-v31.1/README.md',
    'third_party/dependencies/r-efi/AUTHORS',
    'third_party/dependencies/r-efi/README.md'
)
foreach ($relative in $payload + $thirdParty) {
    $source = Join-Path $root $relative
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "required package file missing: $relative" }
    $destination = Join-Path $output $relative
    New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
    Copy-Item -LiteralPath $source -Destination $destination
}
$dependencyLicenseDirectory = Join-Path $output 'dependency-licenses'
& (Join-Path $PSScriptRoot 'collect-dependency-licenses.ps1') `
    -RepositoryRoot $root `
    -Cargo $cargoExe `
    -Go $goExe `
    -RustTarget $RustTarget `
    -RustPackage $riPackage[0].name `
    -OutputDirectory $dependencyLicenseDirectory *> $null
$dependencyLicenseManifest = Get-Content -Raw -LiteralPath (Join-Path $dependencyLicenseDirectory 'dependency-licenses.json') | ConvertFrom-Json
if ($dependencyLicenseManifest.qualification.dependency_evidence_qualified -ne $true -or
    $dependencyLicenseManifest.qualification.package_compliance_qualified -ne $false) {
    throw 'dependency license inventory returned unexpected qualification labels'
}
$help = & $goOutput help 2>&1
if ($LASTEXITCODE -ne 0 -or (($help | Out-String).Trim()).Length -eq 0) { throw 'engorch help smoke failed' }
$riWire = & $riOutput 2>$null
if ($LASTEXITCODE -ne 2) { throw 'engorch-ri usage smoke returned an unexpected status' }
$riEnvelope = (($riWire | Out-String).Trim()) | ConvertFrom-Json
if ($riEnvelope.version -ne 1 -or $riEnvelope.ok -ne $false -or $riEnvelope.error -notmatch '^usage:') {
    throw 'engorch-ri usage smoke returned an unexpected envelope'
}
$sourceAfter = Get-SourceIdentity $root $gitExe
if (($sourceBefore | ConvertTo-Json -Compress) -ne ($sourceAfter | ConvertTo-Json -Compress)) {
    throw 'source changed during package build; package identity is not admissible'
}
$relativeFiles = @("bin/$goName", "bin/$riName") + $payload + $thirdParty + @(Get-ChildItem -LiteralPath $dependencyLicenseDirectory -Recurse -File | ForEach-Object {
    (Get-RelativePathCustom $output $_.FullName).Replace('\', '/')
})
if ((@($relativeFiles | Sort-Object -Unique)).Count -ne $relativeFiles.Count) { throw 'duplicate package file entries detected' }
$records = @($relativeFiles | Sort-Object | ForEach-Object { Get-FileRecord $output $_ })
$sumLines = @($records | ForEach-Object { "$($_.sha256)  $($_.path)" })
$utf8 = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllText((Join-Path $output 'SHA256SUMS'), (($sumLines -join "`n") + "`n"), $utf8)
$records += Get-FileRecord $output 'SHA256SUMS'
$artifactKind = if ($sourceBefore.mode -eq 'commit-clean') { 'local-commit-package' } else { 'local-development-package' }
$manifest = [ordered]@{
    schema_version = 1
    artifact_kind = $artifactKind
    release_qualified = $false
    created_utc = [DateTimeOffset]::UtcNow.ToString('o')
    source = $sourceBefore
    targets = [ordered]@{ goos = $GoOS; goarch = $GoArch; rust = $RustTarget }
    tools = [ordered]@{
        go = [ordered]@{ path = $goExe; sha256 = (Get-FileHash $goExe -Algorithm SHA256).Hash.ToLowerInvariant(); version = Get-ToolVersion $goExe @('version') }
        git = [ordered]@{ path = $gitExe; sha256 = (Get-FileHash $gitExe -Algorithm SHA256).Hash.ToLowerInvariant(); version = Get-ToolVersion $gitExe @('--version') }
        cargo = [ordered]@{ path = $cargoExe; sha256 = (Get-FileHash $cargoExe -Algorithm SHA256).Hash.ToLowerInvariant(); version = Get-ToolVersion $cargoExe @('--version') }
        rustc = [ordered]@{ path = $rustcExe; sha256 = (Get-FileHash $rustcExe -Algorithm SHA256).Hash.ToLowerInvariant(); version = Get-ToolVersion $rustcExe @('--version', '--verbose') }
    }
    components = [ordered]@{
        go_module = ((Get-Content -LiteralPath (Join-Path $root 'go.mod') -TotalCount 1) -replace '^module\s+', '')
        go_binary = $goName
        rust_package = $riPackage[0].name
        rust_version = $riPackage[0].version
        rust_binary = $riName
    }
    dependency_licenses = $dependencyLicenseManifest.qualification
    smoke = [ordered]@{ engorch_help = 'PASS'; engorch_ri_usage_envelope = 'PASS'; platform = "$GoOS/$GoArch" }
    files = @($records | Sort-Object path)
    limitations = @(
        'Local package only; no release, signature, tag, publication or installation was performed.',
        'release_qualified is always false; a clean commit identity is necessary but not sufficient for release qualification.',
        'Build and smoke evidence apply only to the explicitly selected local targets and host.',
        'Collected dependency evidence is target-filtered but does not establish license choice, linker inclusion or legal compliance.'
    )
}
[System.IO.File]::WriteAllText((Join-Path $output 'manifest.json'),
    (($manifest | ConvertTo-Json -Depth 12) + "`n"), $utf8)
& (Join-Path $PSScriptRoot 'verify-local-package.ps1') -PackageDirectory $output
Write-Output ($manifest | ConvertTo-Json -Depth 12 -Compress)
