param()

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem

$scriptsDir = $PSScriptRoot
$verifyScript = Join-Path $scriptsDir 'release-verify.ps1'
$installPsScript = Join-Path $scriptsDir 'install-fabric.ps1'
$installShScript = Join-Path $scriptsDir 'install-fabric.sh'
$buildText = Get-Content -Raw -LiteralPath (Join-Path $scriptsDir 'release-build.ps1')

$fixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('fabric-release-tooling-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixtureRoot | Out-Null

function Get-BytesSha256([byte[]]$Bytes) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $hash = $sha.ComputeHash($Bytes); return ([BitConverter]::ToString($hash)).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}


function New-TinyZip([string]$ZipPath, $Entries) {
    # $Entries: array of @{ Name = '...'; Bytes = [byte[]] }
    $stream = [System.IO.File]::Open($ZipPath, [System.IO.FileMode]::CreateNew)
    try {
        $zip = [System.IO.Compression.ZipArchive]::new($stream, [System.IO.Compression.ZipArchiveMode]::Create, $false)
        try {
            foreach ($e in $Entries) {
                $entry = $zip.CreateEntry($e.Name, [System.IO.Compression.CompressionLevel]::NoCompression)
                $entry.LastWriteTime = [DateTimeOffset]::FromUnixTimeSeconds(1700000000)
                $output = $entry.Open()
                try { $output.Write($e.Bytes, 0, $e.Bytes.Length) } finally { $output.Dispose() }
            }
        } finally { $zip.Dispose() }
    } finally { $stream.Dispose() }
}

function New-FixtureRelease([string]$Directory, [string]$Version, [string]$Commit, [long]$Epoch, [string[]]$Platforms = @('windows-amd64', 'linux-amd64')) {
    New-Item -ItemType Directory -Path $Directory | Out-Null
    $artifacts = @()
    $allPlatformSpecs = @(
        @{ Name = 'windows-amd64'; Os = 'windows'; Arch = 'amd64'; Rust = 'x86_64-pc-windows-gnu'; Ext = '.exe' },
        @{ Name = 'linux-amd64'; Os = 'linux'; Arch = 'amd64'; Rust = 'x86_64-unknown-linux-gnu'; Ext = '' }
    )
    # NOTE: PowerShell variables are case-insensitive, so the filtered
    # collection must NOT be named $platforms: that aliases the
    # [string[]]$Platforms parameter and string-coerces the spec objects
    # (empty Os/Arch produced fabric_0.0.99__.zip duplicates).
    $selectedPlatformSpecs = @($allPlatformSpecs | Where-Object { $Platforms -contains $_.Name })
    if ($selectedPlatformSpecs.Count -ne $Platforms.Count) { throw "unknown fixture platform: $($Platforms -join ', ')" }
    $fixtureToolchain = [ordered]@{ go = 'go version go1.27.1 windows/amd64'; cargo = 'cargo 1.94.1'; rustc = 'rustc 1.94.1' }
    foreach ($p in $selectedPlatformSpecs) {
        $fabricBytes = [System.Text.Encoding]::UTF8.GetBytes("fabric-stub $($p.Os)`n")
        $riBytes = [System.Text.Encoding]::UTF8.GetBytes("fabric-ri-stub $($p.Os)`n")
        $fabricName = 'fabric' + $p.Ext
        $riName = 'fabric-ri' + $p.Ext
        $manifest = [ordered]@{
            schema_version = 1; product = 'Fabric'; version = $Version; commit = $Commit; source_date_epoch = $Epoch
            platform = [ordered]@{ os = $p.Os; arch = $p.Arch; rust_target = $p.Rust }
            toolchain = [ordered]@{ go = $fixtureToolchain.go; cargo = $fixtureToolchain.cargo; rustc = $fixtureToolchain.rustc }
            components = @(
                [ordered]@{ name = 'fabric'; path = $fabricName; sha256 = (Get-BytesSha256 $fabricBytes) },
                [ordered]@{ name = 'fabric-ri'; path = $riName; sha256 = (Get-BytesSha256 $riBytes) }
            )
            release_qualified = $false
            artifact_boundary = 'test fixture only'
        }
        $manifestText = ($manifest | ConvertTo-Json -Depth 8 -Compress) + "`n"
        $manifestBytes = [System.Text.Encoding]::UTF8.GetBytes($manifestText)
        $fileName = "fabric_" + $Version.Substring(1) + "_" + $p.Os + "_" + $p.Arch + ".zip"
        $zipPath = Join-Path $Directory $fileName
        New-TinyZip $zipPath @(
            @{ Name = $fabricName; Bytes = $fabricBytes },
            @{ Name = $riName; Bytes = $riBytes },
            @{ Name = 'manifest.json'; Bytes = $manifestBytes }
        )
        $fileBytes = [System.IO.File]::ReadAllBytes($zipPath)
        $artifacts += [ordered]@{
            file = $fileName
            sha256 = (Get-BytesSha256 $fileBytes)
            bytes = [long]$fileBytes.Length
            platform = "$($p.Os)/$($p.Arch)"
        }
    }
    $artifacts = @($artifacts | Sort-Object file)
    $sums = (($artifacts | ForEach-Object { "$($_.sha256)  $($_.file)" }) -join "`n") + "`n"
    [System.IO.File]::WriteAllText((Join-Path $Directory 'SHA256SUMS'), $sums, [System.Text.UTF8Encoding]::new($false))
    $release = [ordered]@{
        schema_version = 1; product = 'Fabric'; version = $Version; commit = $Commit
        source_date_epoch = $Epoch; platforms = @($Platforms); toolchain = $fixtureToolchain; artifacts = $artifacts; release_qualified = $false
        limitations = @('Unsigned local artifacts. Test fixture only.')
    }
    [System.IO.File]::WriteAllText((Join-Path $Directory 'release.json'), (($release | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    return $Directory
}

function Copy-Directory([string]$Source, [string]$Destination) {
    New-Item -ItemType Directory -Path $Destination | Out-Null
    foreach ($item in (Get-ChildItem -LiteralPath $Source -Force)) {
        Copy-Item -LiteralPath $item.FullName -Destination (Join-Path $Destination $item.Name) -Recurse -Force
    }
}

function Expect-Pass([string]$Name, [scriptblock]$Body) {
    $output = & $Body 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -and $null -eq ($output -match '')) { }
    Write-Output "PASS $Name"
    return $output
}

function Expect-Failure([string]$Name, [scriptblock]$Body, [string]$Pattern) {
    $failed = $false
    try { & $Body 2>&1 | Out-String | Out-Null; if ($LASTEXITCODE -ne 0) { throw "exit code $LASTEXITCODE" } }
    catch {
        $failed = $true
        $message = $_.Exception.Message + ' ' + (($_.FullyQualifiedErrorId | Out-String))
        if ($message -notmatch $Pattern) {
            # Also match against script error text via $Error? Fall back to generic failure acceptance
            # only if pattern was about the same class; otherwise report.
            throw "$Name failed with an unexpected error: $($_.Exception.Message)"
        }
        Write-Output "PASS $Name"
    }
    if (-not $failed) { throw "$Name unexpectedly succeeded" }
}

try {
    $version = 'v0.0.99'
    $commit = '0123456789abcdef0123456789abcdef01234567'
    $epoch = [long]1700000000

    # Static safety properties (no network, integrity wording, atomic rename).
    # NOTE: match against code without "#" comments so explanatory comments
    # (e.g. noting that [Convert]::ToHexString requires .NET 5+) do not
    # trigger negative checks and cannot satisfy positive checks alone.
    function Get-CodeWithoutComments([string]$Text) {
        $codeLines = @()
        foreach ($line in ($Text -split "`r?`n")) {
            if ($line.TrimStart().StartsWith('#')) { continue }
            $hashIndex = $line.IndexOf('#')
            if ($hashIndex -ge 0) { $codeLines += $line.Substring(0, $hashIndex) }
            else { $codeLines += $line }
        }
        return ($codeLines -join "`n")
    }
    foreach ($file in @($verifyScript, $installPsScript, $installShScript)) {
        $text = Get-Content -Raw -LiteralPath $file
        $code = Get-CodeWithoutComments $text
        if ($code -match 'Invoke-WebRequest|Invoke-RestMethod|\bcurl\b|\bwget\b|providers?\.json|https?://') { throw "network/download call found in $(Split-Path -Leaf $file)" }
    }
    $verifyText = Get-Content -Raw -LiteralPath $verifyScript
    $verifyCode = Get-CodeWithoutComments $verifyText
    if ($verifyCode -match '\[Convert\]::ToHexString\s*\(') { throw 'verifier must not invoke ToHexString (PowerShell 5.1 incompatible)' }
    if ($verifyCode -match 'signing|authenticity|signed release|published release') { throw 'verifier must describe integrity, not signing/authenticity' }
    $buildCode = Get-CodeWithoutComments $buildText
    if ($buildCode -notmatch "'-buildvcs=true'" -or $buildCode -match "'-buildvcs=false'") { throw 'release binaries must retain clean VCS provenance for native evaluation' }
    if ($buildCode -notmatch 'NoCompression') { throw 'release build must use NoCompression for reproducibility' }
    if ($buildCode -notmatch 'StringComparer\]::Ordinal') { throw 'release build must sort ZIP entries with ordinal comparison' }
    if ($buildCode -notmatch 'CARGO_ENCODED_RUSTFLAGS' -or $buildCode -match '\$env:RUSTFLAGS\s*=\s*"--remap') { throw 'release build must use CARGO_ENCODED_RUSTFLAGS without clobbering RUSTFLAGS' }
    $installPsText = Get-Content -Raw -LiteralPath $installPsScript
    $installPsCode = Get-CodeWithoutComments $installPsText
    if ($installPsCode -notmatch 'ReparsePoint') { throw 'PowerShell installer must guard symlink/reparse parents' }
    if ($installPsCode -notmatch 'must not already exist') { throw 'PowerShell installer must require an absent target' }
    if ($installPsCode -match 'Move-Item -LiteralPath \$source') { throw 'PowerShell installer must not move binaries singly' }
    if ($installPsCode -notmatch 'Move-Item -LiteralPath \$resolvedStage -Destination \$installRoot') { throw 'PowerShell installer must atomically rename staging directory' }
    $installShText = Get-Content -Raw -LiteralPath $installShScript
    $installShCode = Get-CodeWithoutComments $installShText
    if ($installShCode -notmatch 'must not already exist') { throw 'shell installer must require an absent target' }
    if ($installShCode -notmatch 'os\.rename') { throw 'shell installer must atomically rename staging directory' }
    if ($installShCode -notmatch 'islink') { throw 'shell installer must guard symlink parents' }
    if ($installShCode -notmatch 'component path mismatch') { throw 'shell installer must enforce strict component mapping' }
    # Release-build practicality: explicit platforms, native default, no
    # misleading partial bundles, toolchain record, post-build clean recheck.
    # Static only here: no heavy Go/Cargo builds run in this regression test.
    if ($buildCode -notmatch 'Platforms') { throw 'release build must declare an explicit platform selector' }
    if ($buildCode -notmatch 'windows-amd64' -or $buildCode -notmatch 'linux-amd64') { throw 'release build must support windows-amd64/linux-amd64' }
    if ($buildCode -notmatch 'unsupported platform' -or $buildCode -notmatch 'duplicate platform') { throw 'release build must validate the declared platform list' }
    if ($buildCode -notmatch 'GOOS' -or $buildCode -notmatch 'native host only|go env GOOS') { throw 'release build must default to the native host only' }
    if ($buildCode -notmatch 'x86_64-linux-gnu-gcc' -or $buildCode -notmatch 'native Visual Studio toolchain') { throw 'release build must fail clearly when a cross linker is unavailable' }
    if ($buildCode -notmatch 'partial bundle') { throw 'release build must document the no-partial-bundle rule' }
    if ($buildCode -notmatch 'go version' -or $buildCode -notmatch 'cargo --version' -or $buildCode -notmatch 'rustc --version') { throw 'release build must record Go/Cargo/rustc versions' }
    if ($buildCode -notmatch 'changed during the release build') { throw 'release build must recheck a clean tree before final metadata' }
    # Case-collision regression: PowerShell variables are case-insensitive, so
    # a $platforms local would alias the [string[]]$Platforms parameter and
    # string-coerce the platform specs (empty Os/Arch, fabric_*__.zip dupes).
    # Case-sensitive match: $supportedPlatforms must not trip this check.
    if ($buildCode -cmatch '\$platforms\s*=') { throw 'release build has a case-colliding $platforms variable aliasing $Platforms' }
    if ($buildCode -cnotmatch '\$selectedPlatformSpecs') { throw 'release build must use a distinctly named filtered platform collection' }
    $verifyCodeCheck = $verifyCode
    if ($verifyCodeCheck -notmatch 'one or two explicitly declared platform archives') { throw 'verifier must accept declared one-platform manifests' }
    if ($verifyCodeCheck -notmatch 'rust_target|Rust target') { throw 'verifier must check the declared Rust target per archive' }
    Write-Output 'PASS static safety properties'

    $good = New-FixtureRelease (Join-Path $fixtureRoot 'good') $version $commit $epoch
    # Case-collision regression: exact archive names prove Os/Arch survived
    # (the bug produced fabric_0.0.99__.zip duplicates instead).
    $versionSuffix = $version.Substring(1)
    foreach ($expectedZip in @("fabric_${versionSuffix}_windows_amd64.zip", "fabric_${versionSuffix}_linux_amd64.zip")) {
        if (-not (Test-Path -LiteralPath (Join-Path $good $expectedZip) -PathType Leaf)) { throw "fixture archive missing or misnamed (case-collision regression): $expectedZip" }
    }
    if (@(Get-ChildItem -LiteralPath $good -Filter '*__.zip' -Force).Count -ne 0) { throw 'fixture produced an empty-Os/Arch archive name (case-collision regression)' }

    $verifyOut = & $verifyScript -ReleaseDirectory $good 2>&1 | Out-String
    if ($verifyOut -notmatch 'PASS release integrity') { throw "legitimate fixture failed verification: $verifyOut" }
    Write-Output 'PASS legitimate fixture verifies'

    # Exercise the embedded Python validator on this host. This does not
    # qualify the Linux shell entrypoint or executable loader.
    $pythonCommand = Get-Command python -ErrorAction SilentlyContinue
    if ($null -ne $pythonCommand) {
        $shellSource = Get-Content -LiteralPath $installShScript -Raw
        $pythonSource = ($shellSource -split "<<'PY'\r?\n", 2)[1] -replace '\r?\nPY\s*$', ''
        $pythonFixture = Join-Path $fixtureRoot 'shell-validator.py'
        [System.IO.File]::WriteAllText($pythonFixture, $pythonSource, [System.Text.UTF8Encoding]::new($false))
        $pythonTarget = Join-Path $fixtureRoot 'python-validation-install'
        $pythonOutput = & $pythonCommand.Source $pythonFixture $good $pythonTarget 'linux/amd64' 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath (Join-Path $pythonTarget 'fabric'))) { throw "Python installer validation failed: $pythonOutput" }
        Write-Output 'PASS embedded Python installer validation (host only)'
        $extraSums = Join-Path $fixtureRoot 'bad-extra-sums'
        Copy-Directory $good $extraSums
        Add-Content -LiteralPath (Join-Path $extraSums 'SHA256SUMS') -Value (('0' * 64) + '  extra.zip')
        $pythonOutput = & $pythonCommand.Source $pythonFixture $extraSums (Join-Path $fixtureRoot 'python-invalid-install') 'linux/amd64' 2>&1 | Out-String
        if ($LASTEXITCODE -eq 0 -or $pythonOutput -notmatch 'SHA256SUMS differ from release artifact set') { throw "Python checksum-set rejection failed: $pythonOutput" }
        if (Test-Path -LiteralPath (Join-Path $fixtureRoot 'python-invalid-install')) { throw 'Python checksum-set rejection left an install behind' }
        Write-Output 'PASS embedded Python installer rejects extra checksum entries'
    } else {
        Write-Output 'NOT RUN embedded Python installer validation: python unavailable'
    }

    # Alias spellings must not conceal an omitted platform.
    $duplicatePlatforms = Join-Path $fixtureRoot 'bad-duplicate-platforms'
    Copy-Directory $good $duplicatePlatforms
    $duplicateRelease = Get-Content -Raw -LiteralPath (Join-Path $duplicatePlatforms 'release.json') | ConvertFrom-Json
    $duplicateRelease.platforms = @('windows-amd64', 'windows/amd64')
    [System.IO.File]::WriteAllText((Join-Path $duplicatePlatforms 'release.json'), (($duplicateRelease | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    Expect-Failure 'verifier rejects duplicate declared platform aliases' { & $verifyScript -ReleaseDirectory $duplicatePlatforms } 'duplicate declared platform'

    # Declared platform constraints: one-platform manifests verify with exact
    # component mapping; undeclared or mismatched platforms are rejected.
    $winOnly = New-FixtureRelease (Join-Path $fixtureRoot 'good-winonly') $version $commit $epoch @('windows-amd64')
    if (-not (Test-Path -LiteralPath (Join-Path $winOnly "fabric_${versionSuffix}_windows_amd64.zip") -PathType Leaf)) { throw 'windows-only fixture archive misnamed (case-collision regression)' }
    $winOnlyOut = & $verifyScript -ReleaseDirectory $winOnly 2>&1 | Out-String
    if ($winOnlyOut -notmatch 'PASS release integrity') { throw "windows-only fixture failed verification: $winOnlyOut" }
    Write-Output 'PASS windows-only declared platform verifies'
    $linuxOnly = New-FixtureRelease (Join-Path $fixtureRoot 'good-linuxonly') $version $commit $epoch @('linux-amd64')
    if (-not (Test-Path -LiteralPath (Join-Path $linuxOnly "fabric_${versionSuffix}_linux_amd64.zip") -PathType Leaf)) { throw 'linux-only fixture archive misnamed (case-collision regression)' }
    $linuxOnlyOut = & $verifyScript -ReleaseDirectory $linuxOnly 2>&1 | Out-String
    if ($linuxOnlyOut -notmatch 'PASS release integrity') { throw "linux-only fixture failed verification: $linuxOnlyOut" }
    Write-Output 'PASS linux-only declared platform verifies'
    $badPlatform = Join-Path $fixtureRoot 'bad-platform'
    Copy-Directory $winOnly $badPlatform
    $badRelease = Get-Content -Raw -LiteralPath (Join-Path $badPlatform 'release.json') | ConvertFrom-Json
    $badRelease.artifacts[0].platform = 'darwin/arm64'
    [System.IO.File]::WriteAllText((Join-Path $badPlatform 'release.json'), (($badRelease | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    Expect-Failure 'verifier rejects undeclared platform' { & $verifyScript -ReleaseDirectory $badPlatform } 'invalid artifact platform|release platform set is invalid'
    $badTarget = Join-Path $fixtureRoot 'bad-target'
    Copy-Directory $winOnly $badTarget
    $badZipName = "fabric_" + $version.Substring(1) + "_windows_amd64.zip"
    $badZipPath = Join-Path $badTarget $badZipName
    # Rewrite the archive manifest with a mismatched Rust target; rehash the
    # archive and metadata so the failure is the target check itself.
    Add-Type -AssemblyName System.IO.Compression | Out-Null
    $readZip = [System.IO.Compression.ZipFile]::OpenRead($badZipPath)
    try {
        $reader = [System.IO.StreamReader]::new($readZip.GetEntry('manifest.json').Open(), [System.Text.Encoding]::UTF8, $true)
        try { $badManifest = $reader.ReadToEnd() | ConvertFrom-Json } finally { $reader.Dispose() }
        $fabricHash = $null; $riHash = $null
        foreach ($c in $badManifest.components) { if ($c.name -eq 'fabric') { $fabricHash = $c.sha256 } else { $riHash = $c.sha256 } }
    } finally { $readZip.Dispose() }
    Remove-Item -LiteralPath $badZipPath -Force
    $wrongManifest = [ordered]@{
        schema_version = 1; product = 'Fabric'; version = $version; commit = $commit; source_date_epoch = $epoch
        platform = [ordered]@{ os = 'windows'; arch = 'amd64'; rust_target = 'x86_64-unknown-linux-gnu' }
        toolchain = [ordered]@{ go = 'go version go1.27.1 windows/amd64'; cargo = 'cargo 1.94.1'; rustc = 'rustc 1.94.1' }
        components = @(
            [ordered]@{ name = 'fabric'; path = 'fabric.exe'; sha256 = $fabricHash },
            [ordered]@{ name = 'fabric-ri'; path = 'fabric-ri.exe'; sha256 = $riHash }
        )
        release_qualified = $false; artifact_boundary = 'test fixture only'
    }
    $wrongBytes = [System.Text.Encoding]::UTF8.GetBytes((($wrongManifest | ConvertTo-Json -Depth 8 -Compress) + "`n"))
    $fz = [System.Text.Encoding]::UTF8.GetBytes("fabric-stub windows`n")
    $rz = [System.Text.Encoding]::UTF8.GetBytes("fabric-ri-stub windows`n")
    New-TinyZip $badZipPath @(
        @{ Name = 'fabric.exe'; Bytes = $fz },
        @{ Name = 'fabric-ri.exe'; Bytes = $rz },
        @{ Name = 'manifest.json'; Bytes = $wrongBytes }
    )
    $rebased = [System.IO.File]::ReadAllBytes($badZipPath)
    $reHash = Get-BytesSha256 $rebased
    $badRelease2 = Get-Content -Raw -LiteralPath (Join-Path $badTarget 'release.json') | ConvertFrom-Json
    foreach ($a in $badRelease2.artifacts) { if ($a.file -eq $badZipName) { $a.sha256 = $reHash; $a.bytes = [long]$rebased.Length } }
    [System.IO.File]::WriteAllText((Join-Path $badTarget 'release.json'), (($badRelease2 | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    $reSums = @()
    foreach ($a in $badRelease2.artifacts) { $reSums += "$($a.sha256)  $($a.file)" }
    $reSums = @($reSums | Sort-Object)
    [System.IO.File]::WriteAllText((Join-Path $badTarget 'SHA256SUMS'), (($reSums -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
    Expect-Failure 'verifier rejects Rust target mismatch' { & $verifyScript -ReleaseDirectory $badTarget } 'Rust target mismatch'

    # Legitimate PowerShell install into an absent target.
    $installTarget = Join-Path $fixtureRoot 'install-good-target'
    $installOut = & $installPsScript -ReleaseDirectory $good -InstallDirectory $installTarget 2>&1 | Out-String
    if ($installOut -notmatch 'PASS installed Fabric') { throw "legitimate install failed: $installOut" }
    foreach ($name in @('fabric.exe', 'fabric-ri.exe')) {
        if (-not (Test-Path -LiteralPath (Join-Path $installTarget $name) -PathType Leaf)) { throw "installed payload missing $name" }
    }
    $leftovers = @(Get-ChildItem -LiteralPath $fixtureRoot -Force -Filter '.fabric-install-*')
    if ($leftovers.Count -ne 0) { throw 'staging directory was not cleaned up' }
    Write-Output 'PASS legitimate install'

    # Target must be absent: second install to the same path must fail.
    Expect-Failure 'installer rejects existing target' { & $installPsScript -ReleaseDirectory $good -InstallDirectory $installTarget } 'must not already exist'

    # Traversal archive.
    $traversal = Join-Path $fixtureRoot 'bad-traversal'
    Copy-Directory $good $traversal
    $winZip = Join-Path $traversal ("fabric_" + $version.Substring(1) + "_windows_amd64.zip")
    Remove-Item -LiteralPath $winZip -Force
    $fabricBytes = [System.Text.Encoding]::UTF8.GetBytes("fabric-stub windows`n")
    $riBytes = [System.Text.Encoding]::UTF8.GetBytes("fabric-ri-stub windows`n")
    $manifestObj = [ordered]@{
        schema_version = 1; product = 'Fabric'; version = $version; commit = $commit; source_date_epoch = $epoch
        platform = [ordered]@{ os = 'windows'; arch = 'amd64'; rust_target = 'x86_64-pc-windows-gnu' }
        components = @(
            [ordered]@{ name = 'fabric'; path = 'fabric.exe'; sha256 = (Get-BytesSha256 $fabricBytes) },
            [ordered]@{ name = 'fabric-ri'; path = 'fabric-ri.exe'; sha256 = (Get-BytesSha256 $riBytes) }
        )
        release_qualified = $false; artifact_boundary = 'test fixture only'
    }
    $manifestBytes = [System.Text.Encoding]::UTF8.GetBytes((($manifestObj | ConvertTo-Json -Depth 8 -Compress) + "`n"))
    New-TinyZip $winZip @(
        @{ Name = 'fabric.exe'; Bytes = $fabricBytes },
        @{ Name = 'fabric-ri.exe'; Bytes = $riBytes },
        @{ Name = 'manifest.json'; Bytes = $manifestBytes },
        @{ Name = '../evil.exe'; Bytes = [System.Text.Encoding]::UTF8.GetBytes('evil') }
    )
    $newBytes = [System.IO.File]::ReadAllBytes($winZip)
    $newHash = Get-BytesSha256 $newBytes
    $releaseObj = Get-Content -Raw -LiteralPath (Join-Path $traversal 'release.json') | ConvertFrom-Json
    foreach ($a in $releaseObj.artifacts) { if ($a.file -eq (Split-Path -Leaf $winZip)) { $a.sha256 = $newHash; $a.bytes = [long]$newBytes.Length } }
    [System.IO.File]::WriteAllText((Join-Path $traversal 'release.json'), (($releaseObj | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    $sumsLines = @()
    foreach ($a in $releaseObj.artifacts) { $sumsLines += "$($a.sha256)  $($a.file)" }
    $sumsLines = @($sumsLines | Sort-Object)
    [System.IO.File]::WriteAllText((Join-Path $traversal 'SHA256SUMS'), (($sumsLines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
    Expect-Failure 'verifier rejects traversal entry' { & $verifyScript -ReleaseDirectory $traversal } 'unsafe or duplicate'

    # Duplicate entry archive.
    $dup = Join-Path $fixtureRoot 'bad-duplicate'
    Copy-Directory $good $dup
    $dupZip = Join-Path $dup ("fabric_" + $version.Substring(1) + "_windows_amd64.zip")
    Remove-Item -LiteralPath $dupZip -Force
    New-TinyZip $dupZip @(
        @{ Name = 'fabric.exe'; Bytes = $fabricBytes },
        @{ Name = 'fabric.exe'; Bytes = $fabricBytes },
        @{ Name = 'fabric-ri.exe'; Bytes = $riBytes },
        @{ Name = 'manifest.json'; Bytes = $manifestBytes }
    )
    $dupBytes = [System.IO.File]::ReadAllBytes($dupZip)
    $dupHash = Get-BytesSha256 $dupBytes
    $releaseObj = Get-Content -Raw -LiteralPath (Join-Path $dup 'release.json') | ConvertFrom-Json
    foreach ($a in $releaseObj.artifacts) { if ($a.file -eq (Split-Path -Leaf $dupZip)) { $a.sha256 = $dupHash; $a.bytes = [long]$dupBytes.Length } }
    [System.IO.File]::WriteAllText((Join-Path $dup 'release.json'), (($releaseObj | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    $sumsLines = @()
    foreach ($a in $releaseObj.artifacts) { $sumsLines += "$($a.sha256)  $($a.file)" }
    $sumsLines = @($sumsLines | Sort-Object)
    [System.IO.File]::WriteAllText((Join-Path $dup 'SHA256SUMS'), (($sumsLines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
    Expect-Failure 'verifier rejects duplicate entry' { & $verifyScript -ReleaseDirectory $dup } 'unsafe or duplicate|unexpected or missing'

    # Swapped name/path components.
    $swapped = Join-Path $fixtureRoot 'bad-swapped'
    Copy-Directory $good $swapped
    $swapZip = Join-Path $swapped ("fabric_" + $version.Substring(1) + "_windows_amd64.zip")
    Remove-Item -LiteralPath $swapZip -Force
    $swapManifest = [ordered]@{
        schema_version = 1; product = 'Fabric'; version = $version; commit = $commit; source_date_epoch = $epoch
        platform = [ordered]@{ os = 'windows'; arch = 'amd64'; rust_target = 'x86_64-pc-windows-gnu' }
        components = @(
            [ordered]@{ name = 'fabric'; path = 'fabric-ri.exe'; sha256 = (Get-BytesSha256 $riBytes) },
            [ordered]@{ name = 'fabric-ri'; path = 'fabric.exe'; sha256 = (Get-BytesSha256 $fabricBytes) }
        )
        release_qualified = $false; artifact_boundary = 'test fixture only'
    }
    $swapManifestBytes = [System.Text.Encoding]::UTF8.GetBytes((($swapManifest | ConvertTo-Json -Depth 8 -Compress) + "`n"))
    New-TinyZip $swapZip @(
        @{ Name = 'fabric.exe'; Bytes = $fabricBytes },
        @{ Name = 'fabric-ri.exe'; Bytes = $riBytes },
        @{ Name = 'manifest.json'; Bytes = $swapManifestBytes }
    )
    $swapBytes = [System.IO.File]::ReadAllBytes($swapZip)
    $swapHash = Get-BytesSha256 $swapBytes
    $releaseObj = Get-Content -Raw -LiteralPath (Join-Path $swapped 'release.json') | ConvertFrom-Json
    foreach ($a in $releaseObj.artifacts) { if ($a.file -eq (Split-Path -Leaf $swapZip)) { $a.sha256 = $swapHash; $a.bytes = [long]$swapBytes.Length } }
    [System.IO.File]::WriteAllText((Join-Path $swapped 'release.json'), (($releaseObj | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    $sumsLines = @()
    foreach ($a in $releaseObj.artifacts) { $sumsLines += "$($a.sha256)  $($a.file)" }
    $sumsLines = @($sumsLines | Sort-Object)
    [System.IO.File]::WriteAllText((Join-Path $swapped 'SHA256SUMS'), (($sumsLines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
    Expect-Failure 'verifier rejects swapped components' { & $verifyScript -ReleaseDirectory $swapped } 'path mismatch|invalid component'

    # release_qualified must stay false until acceptance.
    $qualified = Join-Path $fixtureRoot 'bad-qualified'
    Copy-Directory $good $qualified
    $releaseObj = Get-Content -Raw -LiteralPath (Join-Path $qualified 'release.json') | ConvertFrom-Json
    $releaseObj.release_qualified = $true
    [System.IO.File]::WriteAllText((Join-Path $qualified 'release.json'), (($releaseObj | ConvertTo-Json -Depth 8 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    Expect-Failure 'verifier rejects release-qualified claim' { & $verifyScript -ReleaseDirectory $qualified } 'invalid release manifest'

    # Filesystem root guard (no writes to the root itself).
    $fsRoot = [System.IO.Path]::GetPathRoot([System.IO.Path]::GetFullPath($fixtureRoot))
    Expect-Failure 'installer rejects filesystem root' { & $installPsScript -ReleaseDirectory $good -InstallDirectory $fsRoot } 'filesystem root'

    # Symlink parent guard.
    $linkCreated = $false
    $realParent = Join-Path $fixtureRoot 'real-parent'
    New-Item -ItemType Directory -Path $realParent | Out-Null
    $linkParent = Join-Path $fixtureRoot 'link-parent'
    try {
        New-Item -ItemType SymbolicLink -Path $linkParent -Target $realParent -ErrorAction Stop | Out-Null
        $linkCreated = $true
    } catch {
        if ($_.Exception.Message -match 'Privilege|administrat|A required privilege') {
            Write-Output 'NOT RUN installer symlink-parent rejection: symlink creation requires privilege'
        } else { throw }
    }
    if ($linkCreated) {
        $linkTarget = Join-Path $linkParent 'target-absent'
        Expect-Failure 'installer rejects symlink parent' { & $installPsScript -ReleaseDirectory $good -InstallDirectory $linkTarget } 'symlink|reparse'
        if (Test-Path -LiteralPath $linkTarget) { throw 'symlink-parent rejection left a target behind' }
    }

    # Failure atomicity: tampered payload must fail and leave no target behind.
    $tampered = Join-Path $fixtureRoot 'bad-payload'
    Copy-Directory $good $tampered
    $tamperZipName = "fabric_" + $version.Substring(1) + "_windows_amd64.zip"
    $tamperZip = Join-Path $tampered $tamperZipName
    # Corrupt one byte inside the archive without updating any hashes.
    $raw = [System.IO.File]::ReadAllBytes($tamperZip)
    $raw[$raw.Length - 10] = [byte](($raw[$raw.Length - 10] + 1) % 256)
    [System.IO.File]::WriteAllBytes($tamperZip, $raw)
    $atomicTarget = Join-Path $fixtureRoot 'install-atomic-target'
    Expect-Failure 'tampered install fails atomically' { & $installPsScript -ReleaseDirectory $tampered -InstallDirectory $atomicTarget } 'hash|size|checksum|integrity|mismatch|differ'
    if (Test-Path -LiteralPath $atomicTarget) { throw 'failed install left a partial target behind' }
    $stagingLeftovers = @(Get-ChildItem -LiteralPath $fixtureRoot -Force -Filter '.fabric-install-*')
    if ($stagingLeftovers.Count -ne 0) { throw 'failed install left staging behind' }
    Write-Output 'PASS failure atomicity'

    # Shell installer live test only on Linux; elsewhere static checks above suffice.
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Linux)) {
        $shellTarget = Join-Path $fixtureRoot 'install-shell-target'
        & sh $installShScript $good $shellTarget
        if ($LASTEXITCODE -ne 0) { throw 'shell installer failed on legitimate fixture' }
        foreach ($name in @('fabric', 'fabric-ri')) {
            if (-not (Test-Path -LiteralPath (Join-Path $shellTarget $name) -PathType Leaf)) { throw "shell install missing $name" }
        }
        Write-Output 'PASS shell installer legitimate install'
    } else {
        Write-Output 'PASS shell installer static checks (live test Linux-only)'
    }

    Write-Output 'PASS all release tooling tests'
} finally {
    $resolved = [System.IO.Path]::GetFullPath($fixtureRoot)
    $tmpPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if ($resolved.StartsWith($tmpPrefix, [System.StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $resolved)) {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}

# Expected rejection probes deliberately run child processes that return 1.
# Report the suite result rather than inheriting their last exit code.
exit 0
