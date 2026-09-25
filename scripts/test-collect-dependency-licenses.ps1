param()

$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'package-local-compat.ps1')
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$localRoot = [IO.Path]::GetFullPath((Join-Path $root '.local'))
$fixture = Join-Path $localRoot ('collector-fixture-' + [guid]::NewGuid().ToString('N'))

function Write-Utf8([string]$Path, [string]$Content) {
    New-Item -ItemType Directory -Path (Split-Path -Parent $Path) -Force | Out-Null
    [IO.File]::WriteAllText($Path, $Content, [Text.UTF8Encoding]::new($false))
}

function Get-TreeIdentity([string]$Path) {
    return @((Get-ChildItem -LiteralPath $Path -Recurse -File | ForEach-Object {
        $relative = (Get-RelativePathCustom $Path $_.FullName).Replace('\', '/')
        "$relative $((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant())"
    } | Sort-Object) -join "`n")
}

try {
    $repo = Join-Path $fixture 'repo'
    $normal = Join-Path $fixture 'packages/normal'
    $build = Join-Path $fixture 'packages/build'
    $dev = Join-Path $fixture 'packages/dev'
    foreach ($path in @($repo, $normal, $build, $dev)) { New-Item -ItemType Directory -Path $path -Force | Out-Null }
    foreach ($path in @('codegraph', 'graphify', 'scip', 'tgrep', 'opencode', 'vercel-ai', 'pi-subagent-tasks')) {
        New-Item -ItemType Directory -Path (Join-Path $repo "third_party/$path") -Force | Out-Null
    }
    Write-Utf8 (Join-Path $repo 'Cargo.toml') "[workspace]`nmembers=[]`n"
    Write-Utf8 (Join-Path $repo 'go.mod') "module fixture.local/root`n`ngo 1.27`n"
    Write-Utf8 (Join-Path $repo 'go.sum') ''
    Write-Utf8 (Join-Path $repo 'NOTICE') "fixture notice`n"
    Write-Utf8 (Join-Path $repo 'THIRD_PARTY.md') "# Fixture inventory`n"
    Write-Utf8 (Join-Path $repo 'third_party/opencode/LICENSE') "fixture upstream license`n"
    Write-Utf8 (Join-Path $normal 'Cargo.toml') "[package]`nname='normal-dep'`nversion='1.0.0'`n"
    Write-Utf8 (Join-Path $build 'Cargo.toml') "[package]`nname='build-dep'`nversion='2.0.0'`n"
    Write-Utf8 (Join-Path $dev 'Cargo.toml') "[package]`nname='dev-dep'`nversion='3.0.0'`n"
    Write-Utf8 (Join-Path $normal 'LICENSE') "normal license`n"
    Write-Utf8 (Join-Path $dev 'COPYING') "dev license`n"
    Write-Utf8 (Join-Path $repo 'Cargo.lock') @"
version = 4

[[package]]
name = "normal-dep"
version = "1.0.0"
source = "registry+https://example.invalid/index"
checksum = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

[[package]]
name = "build-dep"
version = "2.0.0"
source = "registry+https://example.invalid/index"
checksum = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

[[package]]
name = "dev-dep"
version = "3.0.0"
source = "registry+https://example.invalid/index"
checksum = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
"@

    $rootId = 'path+file:///fixture#engorch-ri@0.1.0'
    $normalId = 'registry+https://example.invalid/index#normal-dep@1.0.0'
    $buildId = 'registry+https://example.invalid/index#build-dep@2.0.0'
    $devId = 'registry+https://example.invalid/index#dev-dep@3.0.0'
    $packages = @(
        [ordered]@{ name='engorch-ri'; version='0.1.0'; id=$rootId; source=$null; license='MIT'; license_file=$null; manifest_path=(Join-Path $repo 'Cargo.toml') },
        [ordered]@{ name='normal-dep'; version='1.0.0'; id=$normalId; source='registry+https://example.invalid/index'; license='MIT'; license_file=$null; manifest_path=(Join-Path $normal 'Cargo.toml') },
        [ordered]@{ name='build-dep'; version='2.0.0'; id=$buildId; source='registry+https://example.invalid/index'; license='MIT'; license_file=$null; manifest_path=(Join-Path $build 'Cargo.toml') },
        [ordered]@{ name='dev-dep'; version='3.0.0'; id=$devId; source='registry+https://example.invalid/index'; license='MIT'; license_file=$null; manifest_path=(Join-Path $dev 'Cargo.toml') }
    )
    $deps = @(
        [ordered]@{ name='normal_dep'; pkg=$normalId; dep_kinds=@([ordered]@{ kind=$null; target=$null }) },
        [ordered]@{ name='build_dep'; pkg=$buildId; dep_kinds=@([ordered]@{ kind='build'; target=$null }) },
        [ordered]@{ name='dev_dep'; pkg=$devId; dep_kinds=@([ordered]@{ kind='dev'; target=$null }) }
    )
    $metadata = [ordered]@{
        packages=$packages
        workspace_root=$repo
        resolve=[ordered]@{ nodes=@(
            [ordered]@{ id=$rootId; deps=$deps },
            [ordered]@{ id=$normalId; deps=@() },
            [ordered]@{ id=$buildId; deps=@() },
            [ordered]@{ id=$devId; deps=@() }
        ) }
    }
    Write-Utf8 (Join-Path $fixture 'metadata.json') ($metadata | ConvertTo-Json -Depth 12)
    Write-Utf8 (Join-Path $fixture 'fake-cargo.ps1') "`$global:LASTEXITCODE=0`nGet-Content -Raw -LiteralPath '$((Join-Path $fixture 'metadata.json').Replace("'", "''"))'`n"
    Write-Utf8 (Join-Path $fixture 'fake-go.ps1') "`$global:LASTEXITCODE=0`n"

    $common = @{
        RepositoryRoot = $repo
        Cargo = Join-Path $fixture 'fake-cargo.ps1'
        Go = Join-Path $fixture 'fake-go.ps1'
        RustTarget = 'fixture-target'
        RustPackage = 'engorch-ri'
    }
    $out1 = Join-Path $fixture 'out1'
    $out2 = Join-Path $fixture 'out2'
    & (Join-Path $PSScriptRoot 'collect-dependency-licenses.ps1') @common -OutputDirectory $out1 > $null
    & (Join-Path $PSScriptRoot 'collect-dependency-licenses.ps1') @common -OutputDirectory $out2 > $null
    if ((Get-TreeIdentity $out1) -ne (Get-TreeIdentity $out2)) { throw 'deterministic fixture outputs differ' }
    $manifest = Get-Content -Raw (Join-Path $out1 'dependency-licenses.json') | ConvertFrom-Json
    $retainedLicense = @(Get-ChildItem -LiteralPath $out1 -Recurse -File | Where-Object {
        $_.FullName.Replace('\', '/').EndsWith('/third_party/opencode/LICENSE', [StringComparison]::Ordinal)
    })
    if ($retainedLicense.Count -ne 1 -or
        (Get-FileHash -LiteralPath $retainedLicense[0].FullName).Hash -ne
        (Get-FileHash -LiteralPath (Join-Path $repo 'third_party/opencode/LICENSE')).Hash) {
        throw 'adapted OpenCode license was not retained byte-for-byte'
    }
    if (@($manifest.rust_packages | Where-Object { $_.closures -contains 'normal' }).Count -ne 1 -or
        @($manifest.rust_packages | Where-Object { $_.closures -contains 'build' }).Count -ne 1 -or
        @($manifest.rust_packages | Where-Object { $_.closures -contains 'dev' }).Count -ne 1) {
        throw 'fixture closure classification failed'
    }
    if ($manifest.qualification.distributed_dependency_evidence_complete -ne $true -or
        $manifest.qualification.build_dependency_evidence_complete -ne $false -or
        $manifest.qualification.package_compliance_qualified -ne $false) {
        throw 'fixture qualification classification failed'
    }
    Write-Output 'PASS deterministic fixture classification and build-only missing evidence'

    Remove-Item -LiteralPath (Join-Path $normal 'LICENSE')
    $missingFailed = $false
    try {
        & (Join-Path $PSScriptRoot 'collect-dependency-licenses.ps1') @common -OutputDirectory (Join-Path $fixture 'out-missing') > $null
    } catch {
        $missingFailed = $_.Exception.Message -match 'normal dependency has no local or retained license evidence'
    }
    if (-not $missingFailed) { throw 'missing normal license evidence did not fail closed' }
    Write-Output 'PASS missing normal dependency evidence fails closed'

    $package = Join-Path $fixture 'package'
    New-Item -ItemType Directory -Path (Join-Path $package 'bin') -Force | Out-Null
    Write-Utf8 (Join-Path $package 'bin/engorch.cmd') "@exit /b 0`n"
    Write-Utf8 (Join-Path $package 'bin/engorch-ri.cmd') "@exit /b 2`n"
    $fileRecords = @()
    foreach ($relative in @('bin/engorch.cmd', 'bin/engorch-ri.cmd')) {
        $path = Join-Path $package $relative
        $fileRecords += [ordered]@{ path=$relative; bytes=[int64](Get-Item $path).Length; sha256=(Get-FileHash $path -Algorithm SHA256).Hash.ToLowerInvariant() }
    }
    $sumText = (($fileRecords | ForEach-Object { "$($_.sha256)  $($_.path)" }) -join "`n") + "`n"
    Write-Utf8 (Join-Path $package 'SHA256SUMS') $sumText
    $fileRecords += [ordered]@{ path='SHA256SUMS'; bytes=[int64](Get-Item (Join-Path $package 'SHA256SUMS')).Length; sha256=(Get-FileHash (Join-Path $package 'SHA256SUMS') -Algorithm SHA256).Hash.ToLowerInvariant() }
    $packageManifest = [ordered]@{
        schema_version=1; artifact_kind='local-development-package'; release_qualified=$false
        source=[ordered]@{ mode='development-unborn'; head=$null; tree=$null; clean=$false }
        components=[ordered]@{ go_binary='engorch.cmd'; rust_binary='engorch-ri.cmd' }
        files=$fileRecords
    }
    Write-Utf8 (Join-Path $package 'manifest.json') (($packageManifest | ConvertTo-Json -Depth 8) + "`n")
    Write-Utf8 (Join-Path $package 'unexpected.txt') "extra`n"
    $extraFailed = $false
    try { & (Join-Path $PSScriptRoot 'verify-local-package.ps1') -PackageDirectory $package > $null }
    catch { $extraFailed = $_.Exception.Message -match 'unlisted or missing files' }
    if (-not $extraFailed) { throw 'package verifier accepted an extra file' }
    Write-Output 'PASS package verifier rejects extra files'
} finally {
    $resolvedFixture = [IO.Path]::GetFullPath($fixture)
    $prefix = $localRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if ($resolvedFixture.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $resolvedFixture)) {
        Remove-Item -LiteralPath $resolvedFixture -Recurse -Force
    }
}
