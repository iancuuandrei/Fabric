<#
.SYNOPSIS
Run one coding task through Fabric's existing controller and inspect its result.
#>
[CmdletBinding(DefaultParameterSetName = 'New')]
param(
    [Parameter(Mandatory)][string]$Fabric,
    [Parameter(Mandatory)][string]$Repository,
    [Parameter(Mandatory, ParameterSetName = 'New')][string]$Objective,
    [Parameter(Mandatory, ParameterSetName = 'Existing')][ValidatePattern('^[a-f0-9]{64}$')][string]$RunId,
    [string]$Actor = [Environment]::UserName,
    [switch]$ApprovePlan,
    [switch]$ApproveChanges,
    [switch]$NonInteractive
)
$ErrorActionPreference = 'Stop'
$Fabric = (Resolve-Path -LiteralPath $Fabric).Path
$Repository = (Resolve-Path -LiteralPath $Repository).Path
if ([string]::IsNullOrWhiteSpace($Actor)) { throw 'An operator Actor is required.' }

function Invoke-Fabric([string[]]$Arguments) {
    Write-Host ('Fabric: ' + $Arguments[0])
    $response = & $Fabric --root $Repository @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Fabric $($Arguments[0]) failed. Inspect status and the exact run before continuing; this script does not retry."
    }
    return (($response -join "`n") | ConvertFrom-Json)
}

function Approve-Step([string]$Question, [bool]$Preapproved) {
    if ($Preapproved) { return $true }
    if ($NonInteractive) { return $false }
    return ((Read-Host "$Question [yes/no]") -ceq 'yes')
}

if ($PSCmdlet.ParameterSetName -eq 'New') {
    $state = Invoke-Fabric @('plan', $Objective)
    $RunId = $state.run_id
} else {
    $state = Invoke-Fabric @('inspect', $RunId)
}
Write-Host "Run: $RunId"
if ($state.lifecycle.status -ne 'ACTIVE') { throw 'Run lifecycle is not ACTIVE; inspect it before continuing.' }

if ($state.state -eq 'AWAITING_APPROVAL') {
    Write-Host $state.plan.output
    if (-not (Approve-Step 'Approve this exact plan?' $ApprovePlan.IsPresent)) {
        return [pscustomobject]@{ run_id = $RunId; state = $state.state; action_required = 'approve plan'; plan_id = $state.plan_id }
    }
    $state = Invoke-Fabric @('approve', $RunId, $state.plan_id, $Actor)
}

if ($state.state -eq 'IMPLEMENTING') {
    if ($null -eq $state.workspace) { $state = Invoke-Fabric @('run', $RunId) }
    if ($state.file_outcome -eq 'UNKNOWN') { throw 'File outcome is UNKNOWN. Inspect/reconcile without repeating the effect.' }
    if ($state.file_outcome -eq 'CONFIRMED' -and
        ($null -eq $state.writer_proposal -or
         $state.file_intent.prepared.intent.input_hash -ne $state.writer_proposal.prepared.intent.input_hash)) {
        throw 'The existing file effect differs from the recorded writer proposal. Inspect this run before continuing.'
    }
    if ($state.file_outcome -ne 'CONFIRMED') {
        if ($null -eq $state.writer_proposal) {
            # An existing runtime intent may represent an uncertain effect. Never
            # issue another writer request from this convenience entry point.
            if ($null -ne $state.writer_host) { throw 'Existing writer attempt requires inspection; no new request was issued.' }
            if ($null -eq $state.explorations -or @($state.explorations).Count -eq 0) {
                if ($null -ne $state.explorer_host) { throw 'Existing explorer attempt requires inspection; no new request was issued.' }
                $null = Invoke-Fabric @('explore', $RunId, 'Inspect the relevant source and tests. Identify the smallest implementation for the approved task and the checks that should validate it.')
            }
            $writer = Invoke-Fabric @('write', $RunId)
        } else {
            $writer = $state.writer_proposal
        }
        $prepared = $writer.prepared
        Write-Host 'Proposed file contents (null content means deletion):'
        foreach ($change in $prepared.proposal.changes) {
            Write-Host "--- $($change.path) (before: $($change.before_hash))"
            if ($null -eq $change.content_base64) { Write-Host '[DELETE]' }
            else { Write-Host ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($change.content_base64))) }
        }
        if (-not (Approve-Step 'Apply these exact changes to the isolated workspace?' $ApproveChanges.IsPresent)) {
            return [pscustomobject]@{ run_id = $RunId; state = 'IMPLEMENTING'; action_required = 'approve changes'; workspace = $state.workspace.request.path }
        }
        # The writer already prepared and journaled its exact proposal. Reuse that
        # approval target rather than generating a different file intent.
        $tempDirectory = Join-Path ([IO.Path]::GetTempPath()) ('fabric-preview-' + [Guid]::NewGuid().ToString('N'))
        $null = New-Item -ItemType Directory -Path $tempDirectory
        $previewPath = Join-Path $tempDirectory 'files.json'
        $preview = Invoke-Fabric @('writer-files', $RunId)
        [IO.File]::WriteAllText($previewPath, ($preview | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
        $state = Invoke-Fabric @('apply-files', $RunId, $previewPath, $preview.intent_id, $Actor)
    }
    $state = Invoke-Fabric @('verify', $RunId)
}

if ($state.state -eq 'REVIEWING') {
    if ($null -ne $state.review_host -and $null -eq $state.review) { throw 'Existing reviewer attempt requires inspection; no new request was issued.' }
    $null = Invoke-Fabric @('review', $RunId)
    $state = Invoke-Fabric @('inspect', $RunId)
}
$verdict = if ($null -ne $state.review) { $state.review.result.output | ConvertFrom-Json } else { $null }
if ($state.state -ne 'READY' -or $verdict.decision -ne 'approve') {
    throw "Task stopped at $($state.state). Inspect run $RunId for check outcomes or review findings. No repair loop was started."
}
$workspace = $state.workspace.request.path
Write-Host 'Result diff:'
& git -C $workspace diff --stat | ForEach-Object { Write-Host $_ }
if ($LASTEXITCODE -ne 0) { throw 'Could not inspect the result diff.' }
# Include new files in the display without changing the index.
foreach ($change in $state.writer_proposal.prepared.proposal.changes) { Write-Host $change.path }
return [pscustomobject]@{
    run_id = $RunId
    state = $state.state
    review = $verdict.decision
    workspace = $workspace
    inspect = "fabric --root `"$Repository`" inspect $RunId"
    export = "fabric --root `"$Repository`" inspect $RunId --export-jsonl"
}
