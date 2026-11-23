<#
.SYNOPSIS
Real Fabric v1 evaluation runner: Prepare fresh pinned tasks, then Evaluate
them with an explicit provider-gated action. No meta-controller.
.DESCRIPTION
Prepare creates fresh pinned detached clones under an external run root,
excludes Fabric config (.harness/harness.toml) from the candidate diff, runs
a native `go test` preflight (manifest native_argv policy; Windows-only
`-skip ^TestNocmpIntegration$` for go-atomic; explicit file argv for legacy
go-difflib) plus a disposable-copy held-out discriminator with exact
stdout/stderr capture and targeted_assertion / expected_missing_api /
setup_error / pass classification, and writes a sanitized run.json with
provider_calls=0. It never invokes the provider.

Manifest native_argv keeps the full Fabric verification argv including the
`go` executable; local runs strip and validate it at invocation
(Go-ArgvPolicy.ps1) so GoExe never receives a doubled executable.
Evaluate runs only under -Action Evaluate with explicit FabricExe, GoExe,
CodexExe, Model and Effort (default high, user value persisted) plus an
explicit -CandidateCopyExe observation/copy helper (fingerprinted, never
auto-built or relaunched). Before any run is created, the task
harness.toml required checks are set to the explicit manifest native_argv
policy (init defaults to `go test ./...`) and hashed. Native mode runs
`fabric init --codex EXE --model MODEL --effort EFFORT`, then native
`fabric run --autonomous` objective plus inspect/usage/diff evidence
gathering. Fabric and PR5 child calls temporarily prepend the selected Go
directory and verify bare `go` resolves to that exact executable; caller PATH
is restored in finally and the executable hash/binding are recorded. The
`go-source-context-v1/v2` and `go-contract-context-v1/v2/v3` treatments
require an explicit absolute, clean parser path and its lowercase SHA-256 via `-PlannerContextRIExecutable` and
`-PlannerContextRIExecutableSHA256`; no parser is discovered from PATH or the
environment, and both values are bound in the prepared/evaluated receipts.
The opt-in Native-only `-ReviewImpactContext` treatment requires a contract
planner context (`go-contract-context-v1/v2/v3`) and the same pinned parser binding.
Its request is matched across Prepare and Evaluate; the inspected run must
show `review_impact_context_version=1` and one durable context record for the
exact reviewed candidate. Omitting it preserves the previous run argv.
The separate `-CandidateFactsCache` switch is a version-1 cache-policy opt-in
for that same Native review-impact treatment. It requires
`-ReviewImpactContext`, a contract planner context, and the pinned parser, and
must match across Prepare and Evaluate. The receipt distinguishes the request
from the inspected `candidate_facts_cache_version=1` policy; cache-hit
statistics are not exposed and are not inferred.
`-FixerModel` and/or `-FixerEffort` select an independent fixer route only
with an explicit `-AccessConfigPath`. The bounded public config's resolved
path and SHA-256 are bound by Prepare and must be unchanged at Evaluate; the
runner rechecks its bytes immediately before `fabric init`. These Native-only
options are appended to init only when requested. Optional `-ModelPolicyPath`
embeds a bounded JSON model policy through the existing `fabric init
--model-policy` option; it requires the explicit fixer/access treatment and
binds the policy file path, SHA-256, and byte count across Prepare/Evaluate.
The inspected creation config must contain the same policy after typed config
defaults are normalized. `-StrictWriterEdits` selects
the anchored-edits-v3 writer contract and is mutually exclusive with the
existing `-ValidateWriterEdits` v2 switch; its request is bound across
Prepare/Evaluate and checked in the inspected creation config.
Resource-bounded isolated writers are an optional Native-only treatment:
`-IsolatedWriters -IsolationPolicyPath ABSOLUTE_PATH -MaxParallel N`. It is
exclusive with `-ParallelWriters`; the policy path and exact file SHA-256 are
bound at Prepare and must match at Evaluate.
The Native-only `-RepairIntelligence` treatment propagates the existing
`--repair-intelligence` flag and binds version 1 across Prepare/Evaluate and
the replay-validated creation policy. Omitted treatment preserves old argv.
This is the product's bundled structured-repair/reviewer-recheck treatment,
not an isolated test of coverage localization or an automatic profile collector.
PR5Matched mode uses the explicit -PR5BaselineScript with the
supplied baseline exe (init with baseline exe/model first, inspect+usage
with the baseline exe, exact run-identity parsing, diff collected from the
candidate path because the baseline lacks diff). Task PASS requires a
successful run+inspect, READY snapshot, exact current candidate bound by the
helper to the reviewed candidate_id and verification plan candidate_id,
non-pending all-PASS verification, reviewer approve with zero findings and
matching candidate_id+verification_plan_id, plus native upstream
verification AND held-out PASS. Missing evidence is BLOCKED, never rerun.
Hidden acceptance tests run only in the helper's candidate-bound byte-copy
of the finished candidate; the original worktree is never mutated. The helper
receives a bounded projection of the complete inspect snapshot; full inspect
JSON remains retained and drives all evaluation gates.
UNKNOWN outcomes are BLOCKED, never automatically rerun. receipt_matched
rows are completed runtime invocations, not provider calls: provider_calls
stays null unless an actual count exists, while runtime invocations and
ContextUsage provider_usage tokens are recorded separately. Unknown metrics
stay null, never zero. Evaluation provenance records evaluation-time
product_head/dirty, exact binary bytes plus embedded Go vcs metadata,
all runner/helper source hashes, linked prepare run.json bytes, task pins,
and helper binary provenance separately. No raw transcripts or credentials
are published.
#>
[CmdletBinding()]
param(
    [ValidateSet('Prepare', 'Evaluate')][string]$Action = 'Prepare',
    [string]$FabricExe,
    [string]$GoExe = 'D:\dev\Fabric-toolchains\go\1.27.1\go\bin\go.exe',
    [string]$CodexExe,
    [string]$Model,
    [ValidateSet('Native', 'PR5Matched')][string]$EvalMode = 'Native',
    [string]$Effort = 'high',
    [string]$PlannerContext = '',
    [ValidateSet('Default', 'Disabled', 'Enabled')][string]$AgentContext = 'Default',
    [string]$PlannerContextRIExecutable = '',
    [string]$PlannerContextRIExecutableSHA256 = '',
    [switch]$ReviewImpactContext,
    [switch]$RepairIntelligence,
    [switch]$CandidateFactsCache,
    [string]$FixerModel = '',
    [string]$FixerEffort = '',
    [string]$AccessConfigPath = '',
    [ValidateSet('', 'cache-prefix-v1')][string]$PromptRecipe = '',
    [switch]$ParallelWriters,
    [switch]$IsolatedWriters,
    [string]$IsolationPolicyPath = '',
    [switch]$ValidateWriterEdits,
    [switch]$StrictWriterEdits,
    [ValidateRange(0, 8)][int]$MaxParallel = 0,
    [string]$PR5BaselineExe,
    [string]$PR5BaselineScript,
    [string]$CandidateCopyExe,
    [string]$NativeBuildReceiptPath,
    [ValidateRange(0, 10000000)][long]$AutoCompactTokenLimit = 0,
    [string]$RunRoot = 'D:\dev\Fabric-v1-eval-runs',
    [string[]]$TaskIds,
    [string]$RunId,
    [string]$ModelPolicyPath = ''
)

$ErrorActionPreference = 'Stop'
function Assert-AgentContextRunnerBinding([string]$Mode, [string]$Treatment) {
    if ($Treatment -notin @('Default', 'Disabled', 'Enabled')) { throw 'Unsupported agent-context treatment.' }
    if ($Treatment -ne 'Default' -and $Mode -ne 'Native') { throw 'AgentContext treatment requires Native mode.' }
}
Assert-AgentContextRunnerBinding $EvalMode $AgentContext

function Assert-RepairIntelligenceRunnerBinding([string]$Mode, [bool]$Enabled) {
    if ($Enabled -and $Mode -ne 'Native') { throw 'RepairIntelligence requires Native mode.' }
}
Assert-RepairIntelligenceRunnerBinding $EvalMode ([bool]$RepairIntelligence)

function Assert-RepairIntelligencePreparedBinding($Prior, [bool]$Enabled) {
    $requested = $Prior.PSObject.Properties['repair_intelligence_version_requested']
    $version = if ($null -eq $requested) { 0 } else { $requested.Value }
    $expected = if ($Enabled) { 1 } else { 0 }
    if (($version -isnot [int] -and $version -isnot [long]) -or $version -ne $expected) {
        throw 'RepairIntelligence must match the version recorded by the prepared run.'
    }
}

function Assert-RepairIntelligenceObserved($Snapshot, [bool]$Enabled) {
    $execution = $Snapshot.creation.execution
    $version = if ($null -eq $execution.repair_intelligence_version) { 0 } else { $execution.repair_intelligence_version }
    $expected = if ($Enabled) { 1 } else { 0 }
    if (($version -isnot [int] -and $version -isnot [long]) -or $version -ne $expected) {
        throw 'Inspected repair intelligence version differs from the requested treatment.'
    }
    if ($Enabled) {
        foreach ($required in @($execution.graph_version, $execution.repair_planning_version)) {
            if (($required -isnot [int] -and $required -isnot [long]) -or $required -ne 1) {
                throw 'Inspected repair intelligence lacks its required graph/repair/context policy.'
            }
        }
        if ($execution.context -isnot [string] -or $execution.context -cne 'bounded-v1') {
            throw 'Inspected repair intelligence lacks its required graph/repair/context policy.'
        }
        if ($Snapshot.creation.config.reviewer_contract -ceq 'json-v1' -and $null -ne $Snapshot.creation.config.reviewer) {
            $recheck = $execution.review_recheck_version
            if (($recheck -isnot [int] -and $recheck -isnot [long]) -or $recheck -ne 1) {
                throw 'Inspected structured reviewer lacks the bundled recheck policy.'
            }
        }
    }
    return $version
}

function Assert-AgentContextPreparedBinding($Prior, [string]$Treatment) {
    $property = $Prior.PSObject.Properties['agent_context_requested']
    $prepared = if ($null -eq $property) { 'Default' } else { [string]$property.Value }
    if ($prepared -cne $Treatment) { throw 'AgentContext must match the treatment recorded by the prepared run.' }
}

function Assert-AgentContextObserved($Snapshot, [string]$Treatment) {
    $creation = $Snapshot.creation
    $bundle = $creation.agent_context
    if ($Treatment -eq 'Disabled' -and $null -ne $bundle) { throw 'Disabled agent-context treatment retained a bundle.' }
    if ($Treatment -eq 'Enabled' -and $null -eq $bundle) { throw 'Enabled agent-context treatment did not retain a bundle.' }
    if ($null -eq $bundle) { return [ordered]@{ present = $false } }
    if ($bundle.version -ne 1 -or $bundle.source_commit -cne $creation.repository.commit -or
        [string]$bundle.source_id -cnotmatch '^[0-9a-f]{64}$') {
        throw 'Inspected agent-context bundle has an invalid source binding.'
    }
    # Inspect has already validated the canonical source identity and every
    # document hash through Go replay. Counts describe retained inputs only.
    return [ordered]@{
        present = $true; version = $bundle.version
        source_id = $bundle.source_id; source_commit = $bundle.source_commit
        instructions = @($bundle.instructions | Where-Object { $null -ne $_ }).Count
        skills = @($bundle.skills | Where-Object { $null -ne $_ }).Count
    }
}

function Assert-AutoCompactRunnerBinding([string]$Mode, [long]$Limit, [bool]$Explicit) {
    if ($Explicit -and ($Limit -le 0 -or $Limit -gt 10000000)) {
        throw 'AutoCompactTokenLimit must be between 1 and 10000000 when supplied.'
    }
    if ($Mode -eq 'PR5Matched' -and $Explicit) {
        throw 'AutoCompactTokenLimit requires Native mode; PR5Matched retains its original invocation.'
    }
}
Assert-AutoCompactRunnerBinding $EvalMode $AutoCompactTokenLimit ([bool]$PSBoundParameters.ContainsKey('AutoCompactTokenLimit'))
function Assert-FixerAccessRunnerBinding([string]$Mode, [bool]$FixerModelExplicit, [string]$Model, [bool]$FixerEffortExplicit, [string]$Effort, [bool]$AccessConfigExplicit, [string]$AccessConfig) {
    $fixerRequested = $FixerModelExplicit -or $FixerEffortExplicit
    if ($fixerRequested -and $Mode -ne 'Native') { throw 'Fixer model allocation requires Native mode.' }
    if ($FixerModelExplicit -and [string]::IsNullOrWhiteSpace($Model)) { throw 'FixerModel must not be empty when explicitly supplied.' }
    if ($FixerEffortExplicit -and [string]::IsNullOrWhiteSpace($Effort)) { throw 'FixerEffort must not be empty when explicitly supplied.' }
    if ($fixerRequested -ne $AccessConfigExplicit) {
        throw 'FixerModel or FixerEffort requires exactly one AccessConfigPath; AccessConfigPath is only valid with a fixer override.'
    }
    if ($AccessConfigExplicit -and [string]::IsNullOrWhiteSpace($AccessConfig)) { throw 'AccessConfigPath must not be empty.' }
}
$FixerModelExplicit = [bool]$PSBoundParameters.ContainsKey('FixerModel')
$FixerEffortExplicit = [bool]$PSBoundParameters.ContainsKey('FixerEffort')
$AccessConfigExplicit = [bool]$PSBoundParameters.ContainsKey('AccessConfigPath')
Assert-FixerAccessRunnerBinding $EvalMode $FixerModelExplicit $FixerModel $FixerEffortExplicit $FixerEffort $AccessConfigExplicit $AccessConfigPath
$ModelPolicyExplicit = [bool]$PSBoundParameters.ContainsKey('ModelPolicyPath')
function Assert-ModelPolicyRunnerBinding([string]$Mode, [bool]$Explicit, [string]$Path, [bool]$FixerRequested, [bool]$AccessConfigRequested) {
    if (-not $Explicit) { return }
    if ($Mode -ne 'Native') { throw 'ModelPolicyPath requires Native mode.' }
    if ([string]::IsNullOrWhiteSpace($Path)) { throw 'ModelPolicyPath must not be empty when explicitly supplied.' }
    if (-not $FixerRequested -or -not $AccessConfigRequested) { throw 'ModelPolicyPath requires an explicit fixer override and AccessConfigPath.' }
}
Assert-ModelPolicyRunnerBinding $EvalMode $ModelPolicyExplicit $ModelPolicyPath ($FixerModelExplicit -or $FixerEffortExplicit) $AccessConfigExplicit
function Get-WriterContractRequest([bool]$ValidateEnabled, [bool]$StrictEnabled, [string]$Mode) {
    if ($ValidateEnabled -and $StrictEnabled) { throw 'StrictWriterEdits and ValidateWriterEdits are mutually exclusive.' }
    if (($ValidateEnabled -or $StrictEnabled) -and $Mode -ne 'Native') { throw 'Writer contract overrides require Native mode.' }
    if ($StrictEnabled) { return 'anchored-edits-v3' }
    if ($ValidateEnabled) { return 'anchored-edits-v2' }
    return ''
}
$writerContractRequested = Get-WriterContractRequest ([bool]$ValidateWriterEdits) ([bool]$StrictWriterEdits) $EvalMode
function Assert-IsolatedRunnerOptionShape([bool]$Enabled, [string]$PolicyPath, [bool]$Parallel, [string]$Mode, [int]$ParallelLimit) {
    if ($Enabled -ne (-not [string]::IsNullOrWhiteSpace($PolicyPath))) {
        throw 'IsolatedWriters and IsolationPolicyPath must be supplied together.'
    }
    if ($Enabled -and $Parallel) {
        throw 'IsolatedWriters and ParallelWriters are mutually exclusive.'
    }
    if ($Enabled -and $Mode -ne 'Native') {
        throw 'IsolatedWriters requires Native mode.'
    }
    if ($Enabled -and ($ParallelLimit -lt 1 -or $ParallelLimit -gt 8)) {
        throw 'IsolatedWriters requires an explicit MaxParallel value from 1 through 8.'
    }
}
if ($Action -eq 'Evaluate' -and $EvalMode -eq 'PR5Matched' -and ($ParallelWriters -or $ValidateWriterEdits -or $StrictWriterEdits -or $MaxParallel -ne 0)) {
    throw 'Writer and scheduler overrides require Native mode; PR5Matched retains its original invocation.'
}
Assert-IsolatedRunnerOptionShape ([bool]$IsolatedWriters) $IsolationPolicyPath ([bool]$ParallelWriters) $EvalMode $MaxParallel
function Test-GoSourceContextMode([string]$Mode) {
    return $Mode -ceq 'go-source-context-v1' -or $Mode -ceq 'go-source-context-v2' -or $Mode -ceq 'go-contract-context-v1' -or $Mode -ceq 'go-contract-context-v2' -or $Mode -ceq 'go-contract-context-v3'
}
function Assert-ReviewImpactRunnerBindingShape([bool]$Enabled, [string]$PlannerMode, [string]$Mode) {
    if (-not $Enabled) { return }
    if ($Mode -ne 'Native') { throw 'ReviewImpactContext requires Native mode.' }
    if ($PlannerMode -cnotin @('go-contract-context-v1', 'go-contract-context-v2', 'go-contract-context-v3')) {
        throw 'ReviewImpactContext requires a go-contract-context-v1/v2/v3 PlannerContext and its explicit pinned RI parser binding.'
    }
}
function Assert-CandidateFactsCacheRunnerBindingShape([bool]$Enabled, [bool]$ReviewImpactEnabled, [string]$PlannerMode, [string]$Mode) {
    if (-not $Enabled) { return }
    if ($Mode -ne 'Native') { throw 'CandidateFactsCache requires Native mode.' }
    if (-not $ReviewImpactEnabled -or $PlannerMode -cnotin @('go-contract-context-v1', 'go-contract-context-v2', 'go-contract-context-v3')) {
        throw 'CandidateFactsCache requires ReviewImpactContext, a go-contract-context-v1/v2/v3 PlannerContext, and its explicit pinned RI parser binding.'
    }
}
function Assert-PlannerContextBindingShape([string]$Mode, [string]$Executable, [string]$ExecutableSHA256) {
    if ($Mode -cnotin @('', 'source-bounded-v1', 'go-source-context-v1', 'go-source-context-v2', 'go-contract-context-v1', 'go-contract-context-v2', 'go-contract-context-v3')) {
        throw 'PlannerContext must be empty, source-bounded-v1, go-source-context-v1/v2, or go-contract-context-v1/v2/v3.'
    }
    if (Test-GoSourceContextMode $Mode) {
        if ([string]::IsNullOrWhiteSpace($Executable) -or [string]::IsNullOrWhiteSpace($ExecutableSHA256)) {
            throw 'Pinned Go planner context modes require PlannerContextRIExecutable and PlannerContextRIExecutableSHA256.'
        }
        if (-not [IO.Path]::IsPathFullyQualified($Executable) -or [IO.Path]::GetFullPath($Executable) -cne $Executable) {
            throw 'PlannerContextRIExecutable must be an absolute clean path, passed unchanged.'
        }
        if ($ExecutableSHA256 -cnotmatch '^[0-9a-f]{64}$') {
            throw 'PlannerContextRIExecutableSHA256 must be 64 lowercase hexadecimal characters.'
        }
        return
    }
    if ($Executable -ne '' -or $ExecutableSHA256 -ne '') {
        throw 'PlannerContextRIExecutable and PlannerContextRIExecutableSHA256 require a pinned Go planner context mode.'
    }
}
Assert-PlannerContextBindingShape $PlannerContext $PlannerContextRIExecutable $PlannerContextRIExecutableSHA256
Assert-ReviewImpactRunnerBindingShape ([bool]$ReviewImpactContext) $PlannerContext $EvalMode
Assert-CandidateFactsCacheRunnerBindingShape ([bool]$CandidateFactsCache) ([bool]$ReviewImpactContext) $PlannerContext $EvalMode
if ($Action -eq 'Evaluate' -and $EvalMode -eq 'PR5Matched' -and ($PlannerContext -ne '' -or $PlannerContextRIExecutable -ne '' -or $PlannerContextRIExecutableSHA256 -ne '')) {
    throw 'PlannerContext requires Native mode; PR5Matched retains its original invocation.'
}
if ($Action -eq 'Evaluate' -and $EvalMode -eq 'PR5Matched' -and $PromptRecipe -ne '') {
    throw 'PromptRecipe requires Native mode; PR5Matched retains its original invocation.'
}
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$manifestPath = Join-Path $repoRoot 'evals\v1\manifest.json'
$heldoutDir = Join-Path $repoRoot 'evals\v1\heldout'
$defaultRunTaskScript = Join-Path $PSScriptRoot 'run-task.ps1'
$manifest = Get-Content -Raw $manifestPath | ConvertFrom-Json
$git = (Get-Command git -ErrorAction Stop).Source
. (Join-Path $repoRoot 'evals\v1\harness\Classify-CheckOutput.ps1')
# Copy-CandidateTree.ps1 is NOT used for acceptance here: the candidate-bound
# Go helper (-CandidateCopyExe) owns observation/copy under a read lease and
# binds Candidate.ID() to the reviewed candidate and verification plan.
# The PS copy remains only for its standalone fixtures, never as an
# acceptance route (self-only copy would verify the copy against itself).
. (Join-Path $repoRoot 'evals\v1\harness\Go-ArgvPolicy.ps1')
. (Join-Path $repoRoot 'evals\v1\harness\Toml-ArgvPolicy.ps1')

$DiffByteLimit = 1048576

function Get-NativeInitArgs([string]$TaskPath, [string]$RuntimePath, [string]$WriterModel, [string]$WriterEffort, [bool]$EnableEditValidation, [string]$FixerModel = '', [string]$FixerEffort = '', [string]$AccessConfigPath = '', [bool]$EnableStrictWriterEdits = $false, [string]$ModelPolicyPath = '') {
    if ($EnableEditValidation -and $EnableStrictWriterEdits) { throw 'StrictWriterEdits and ValidateWriterEdits are mutually exclusive.' }
    Assert-FixerAccessRunnerBinding 'Native' ($FixerModel -ne '') $FixerModel ($FixerEffort -ne '') $FixerEffort ($AccessConfigPath -ne '') $AccessConfigPath
    Assert-ModelPolicyRunnerBinding 'Native' ($ModelPolicyPath -ne '') $ModelPolicyPath (($FixerModel -ne '') -or ($FixerEffort -ne '')) ($AccessConfigPath -ne '')
    $nativeArgs = @('--root', $TaskPath, 'init', '--codex', $RuntimePath, '--model', $WriterModel, '--effort', $WriterEffort)
    if ($FixerModel -ne '') { $nativeArgs += @('--fixer-model', $FixerModel) }
    if ($FixerEffort -ne '') { $nativeArgs += @('--fixer-effort', $FixerEffort) }
    if ($AccessConfigPath -ne '') { $nativeArgs += @('--access-config', $AccessConfigPath) }
    if ($ModelPolicyPath -ne '') { $nativeArgs += @('--model-policy', $ModelPolicyPath) }
    if ($EnableEditValidation) { $nativeArgs += '--validate-writer-edits' }
    if ($EnableStrictWriterEdits) { $nativeArgs += '--strict-writer-edits' }
    return $nativeArgs
}

function Get-NativeRunArgs([string]$TaskPath, [string]$Objective, [bool]$EnableParallelWriters, [int]$ParallelLimit, [string]$PlannerContext = '', [string]$PromptRecipe = '', [string]$PlannerContextRIExecutable = '', [string]$PlannerContextRIExecutableSHA256 = '', [bool]$EnableIsolatedWriters = $false, [string]$IsolationPolicyPath = '', [long]$AutoCompactTokenLimit = 0, [bool]$EnableReviewImpactContext = $false, [bool]$EnableCandidateFactsCache = $false, [string]$AgentContext = 'Default', [bool]$EnableRepairIntelligence = $false) {
    if ($AgentContext -notin @('Default', 'Disabled', 'Enabled')) { throw 'Unsupported agent-context treatment.' }
    if ($ParallelLimit -lt 0 -or $ParallelLimit -gt 8) { throw 'Scheduler override must be 0 (default) or 1..8.' }
    if ($AutoCompactTokenLimit -lt 0 -or $AutoCompactTokenLimit -gt 10000000) { throw 'AutoCompactTokenLimit must be 0 (omitted) or 1..10000000.' }
    if ($PromptRecipe -notin @('', 'cache-prefix-v1')) { throw 'Unsupported prompt recipe.' }
    Assert-PlannerContextBindingShape $PlannerContext $PlannerContextRIExecutable $PlannerContextRIExecutableSHA256
    Assert-ReviewImpactRunnerBindingShape $EnableReviewImpactContext $PlannerContext 'Native'
    Assert-CandidateFactsCacheRunnerBindingShape $EnableCandidateFactsCache $EnableReviewImpactContext $PlannerContext 'Native'
    if ($EnableParallelWriters -and $EnableIsolatedWriters) { throw 'ParallelWriters and IsolatedWriters are mutually exclusive.' }
    if ($EnableIsolatedWriters -ne (-not [string]::IsNullOrWhiteSpace($IsolationPolicyPath))) { throw 'IsolatedWriters and IsolationPolicyPath must be supplied together.' }
    if ($EnableIsolatedWriters -and ($ParallelLimit -lt 1 -or $ParallelLimit -gt 8)) { throw 'IsolatedWriters requires MaxParallel from 1 through 8.' }
    $nativeArgs = @('--root', $TaskPath, 'run', '--autonomous')
    if ($AgentContext -eq 'Disabled') { $nativeArgs += '--agent-context=false' }
    if ($AgentContext -eq 'Enabled') { $nativeArgs += '--agent-context=true' }
    if ($EnableRepairIntelligence) { $nativeArgs += '--repair-intelligence' }
    if ($PlannerContext -ne '') { $nativeArgs += @('--planner-context', $PlannerContext) }
    if (Test-GoSourceContextMode $PlannerContext) {
        $nativeArgs += @('--planner-context-ri-executable', $PlannerContextRIExecutable, '--planner-context-ri-executable-sha256', $PlannerContextRIExecutableSHA256)
    }
    if ($EnableReviewImpactContext) { $nativeArgs += '--review-impact-context' }
    if ($EnableCandidateFactsCache) { $nativeArgs += '--review-impact-candidate-facts-cache' }
    if ($PromptRecipe -ne '') { $nativeArgs += @('--prompt-recipe', $PromptRecipe) }
    if ($EnableParallelWriters) { $nativeArgs += '--parallel-writers' }
    if ($EnableIsolatedWriters) { $nativeArgs += @('--isolated-writers', '--isolation-policy', $IsolationPolicyPath) }
    if ($ParallelLimit -ne 0) { $nativeArgs += @('--max-parallel', [string]$ParallelLimit) }
    if ($AutoCompactTokenLimit -gt 0) { $nativeArgs += @('--auto-compact-token-limit', [string]$AutoCompactTokenLimit) }
    $nativeArgs += $Objective
    return $nativeArgs
}

function Assert-AutoCompactPreparedBinding($Prior, [long]$Limit) {
    $preparedLimit = 0L
    if ($null -ne $Prior.PSObject.Properties['auto_compact_token_limit_requested'] -and $null -ne $Prior.auto_compact_token_limit_requested) {
        $preparedLimit = [long]$Prior.auto_compact_token_limit_requested
    }
    if ($preparedLimit -ne $Limit) {
        throw 'AutoCompactTokenLimit must match the treatment recorded by the prepared run.'
    }
}

function Assert-AutoCompactObserved($Snapshot, [long]$Limit) {
    $execution = $Snapshot.creation.execution
    $observed = $null
    if ($null -ne $execution) { $observed = $execution.codex_auto_compact }
    if ($Limit -eq 0) {
        if ($null -ne $observed) { throw 'Inspected run unexpectedly enables native Codex automatic compaction.' }
        return $null
    }
    if ($null -eq $observed -or $observed.version -ne 1 -or $observed.token_limit -ne $Limit) {
        throw 'Inspected run auto-compaction option does not match the requested Native treatment.'
    }
    return $observed
}

function Get-GitText([string]$Path, [string[]]$GitArgs) {
    $value = & $git -C $Path @GitArgs
    if ($LASTEXITCODE -ne 0) { throw "git failed in ${Path}: $($GitArgs -join ' ')" }
    return ($value -join "`n").Trim()
}

function Get-FileSha256([string]$Path) {
    $h = [System.Security.Cryptography.SHA256]::Create()
    try {
        $fs = [System.IO.File]::OpenRead($Path)
        try { return ([BitConverter]::ToString($h.ComputeHash($fs)) -replace '-', '').ToLowerInvariant() }
        finally { $fs.Close() }
    } finally { $h.Dispose() }
}

function Read-BoundedConfigFile([string]$Path, [int]$MaxBytes, [string]$PathError, [string]$TypeError, [string]$SizeError, [bool]$RequireCleanPath) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [IO.Path]::IsPathFullyQualified($Path) -or ($RequireCleanPath -and [IO.Path]::GetFullPath($Path) -cne $Path)) {
        throw $PathError
    }
    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw $TypeError
    }
    if ($item.Length -eq 0 -or $item.Length -gt $MaxBytes) { throw $SizeError }
    $stream = [IO.File]::Open($item.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        if ($stream.Length -eq 0 -or $stream.Length -gt $MaxBytes) { throw $SizeError }
        $buffer = New-Object byte[] ($MaxBytes + 1)
        $read = $stream.Read($buffer, 0, $buffer.Length)
        if ($read -eq 0 -or $read -gt $MaxBytes -or $stream.ReadByte() -ne -1) {
            throw $SizeError
        }
        [byte[]]$bytes = $buffer[0..($read - 1)]
    } finally { $stream.Dispose() }
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { $sha256 = ([BitConverter]::ToString($hasher.ComputeHash($bytes)) -replace '-', '').ToLowerInvariant() }
    finally { $hasher.Dispose() }
    return [pscustomobject]@{ Path = $item.FullName; Sha256 = $sha256; Bytes = $bytes.Length; Content = $bytes }
}

function Get-AccessConfigBinding([string]$Path) {
    $file = Read-BoundedConfigFile $Path (32 * 1024) 'AccessConfigPath must be an absolute file path.' 'AccessConfigPath must name a regular non-reparse file.' 'Access config must be nonempty and no larger than 32 KiB.' $false
    return [ordered]@{ Path = $file.Path; Sha256 = $file.Sha256; Bytes = $file.Bytes }
}

function Get-ModelPolicyBinding([string]$Path) {
    $file = Read-BoundedConfigFile $Path (128 * 1024) 'ModelPolicyPath must be an absolute clean path passed unchanged.' 'ModelPolicyPath must name a regular non-reparse file.' 'Model policy must be nonempty and no larger than 128 KiB.' $true
    $strictUtf8 = New-Object System.Text.UTF8Encoding($false, $true)
    try { $json = $strictUtf8.GetString($file.Content) } catch { throw 'Model policy must be valid UTF-8 JSON.' }
    try { $policy = ConvertFrom-Json -InputObject $json -AsHashtable -ErrorAction Stop } catch { throw 'Model policy must be valid JSON.' }
    if ($null -eq $policy -or $policy -isnot [System.Collections.IDictionary]) { throw 'Model policy JSON must contain an object.' }
    return [ordered]@{ Path = $file.Path; Sha256 = $file.Sha256; Bytes = $file.Bytes; Policy = $policy }
}

function Assert-CurrentModelPolicyBinding($Binding) {
    if ($null -eq $Binding) { throw 'Model policy binding is missing.' }
    $current = Get-ModelPolicyBinding $Binding.Path
    if ($current.Path -cne $Binding.Path -or $current.Sha256 -cne $Binding.Sha256 -or $current.Bytes -ne $Binding.Bytes) {
        throw 'Model policy path or file bytes changed after Prepare.'
    }
}

function Assert-CurrentAccessConfigBinding($Binding) {
    if ($null -eq $Binding) { throw 'Access config binding is missing.' }
    $current = Get-AccessConfigBinding $Binding.Path
    if ($current.Path -cne $Binding.Path -or $current.Sha256 -cne $Binding.Sha256 -or $current.Bytes -ne $Binding.Bytes) {
        throw 'Access config path or file bytes changed after Prepare.'
    }
}

function Assert-FixerAccessPreparedBinding($Prior, [bool]$FixerModelExplicit, [string]$Model, [bool]$FixerEffortExplicit, [string]$Effort, $Binding) {
    $expected = @{
        fixer_model_requested = if ($FixerModelExplicit) { $Model } else { $null }
        fixer_effort_requested = if ($FixerEffortExplicit) { $Effort } else { $null }
        access_config_path_requested = if ($null -ne $Binding) { $Binding.Path } else { $null }
        access_config_sha256_requested = if ($null -ne $Binding) { $Binding.Sha256 } else { $null }
        access_config_bytes_requested = if ($null -ne $Binding) { [int]$Binding.Bytes } else { $null }
    }
    foreach ($name in $expected.Keys) {
        $property = $Prior.PSObject.Properties[$name]
        $actual = if ($null -ne $property) { $property.Value } else { $null }
        if ($null -eq $expected[$name]) {
            if ($null -ne $actual -and $actual -ne '') { throw 'Fixer model/access config options must match the prepared run.' }
        } elseif ([string]$actual -cne [string]$expected[$name]) {
            throw 'Fixer model/access config options must match the prepared run.'
        }
    }
}

function Assert-ModelPolicyPreparedBinding($Prior, [bool]$Explicit, $Binding) {
    $expected = @{
        model_policy_path_requested = if ($null -ne $Binding) { $Binding.Path } else { $null }
        model_policy_sha256_requested = if ($null -ne $Binding) { $Binding.Sha256 } else { $null }
        model_policy_bytes_requested = if ($null -ne $Binding) { [int]$Binding.Bytes } else { $null }
    }
    if ($Explicit -ne ($null -ne $Binding)) { throw 'Model policy options must match the prepared run.' }
    foreach ($name in $expected.Keys) {
        $property = $Prior.PSObject.Properties[$name]
        $actual = if ($null -ne $property) { $property.Value } else { $null }
        if ($null -eq $expected[$name]) {
            if ($null -ne $actual -and $actual -ne '') { throw 'Model policy options must match the prepared run.' }
        } elseif ([string]$actual -cne [string]$expected[$name]) {
            throw 'Model policy options must match the prepared run.'
        }
    }
}

function ConvertTo-ComparableModelPolicyValue($Value) {
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        $result = [ordered]@{}
        foreach ($rawName in ($Value.Keys | Sort-Object -CaseSensitive)) {
            $name = ([string]$rawName).ToLowerInvariant() -replace '[_-]', ''
            $entry = $Value[$rawName]
            if ($null -eq $entry) { continue }
            # Go's typed JSON encoding omits these zero-valued optional fields.
            if (($name -eq 'decisionevidenceversion' -or $name -eq 'cheapcontextbytes' -or $name -eq 'contextescalationbytes') -and [long]$entry -eq 0) { continue }
            if ($name -eq 'cheapprofile' -and [string]$entry -eq '') { continue }
            if ($name -eq 'observedfixerprofiles' -and @($entry).Count -eq 0) { continue }
            $result[$name] = ConvertTo-ComparableModelPolicyValue $entry
        }
        return ,$result
    }
    if ($Value -is [pscustomobject]) {
        $asMap = [ordered]@{}
        foreach ($property in $Value.PSObject.Properties) { $asMap[$property.Name] = $property.Value }
        return ,(ConvertTo-ComparableModelPolicyValue $asMap)
    }
    if ($Value -is [System.Collections.IEnumerable] -and $Value -isnot [string]) {
        $items = [System.Collections.Generic.List[object]]::new()
        foreach ($entry in $Value) { $items.Add((ConvertTo-ComparableModelPolicyValue $entry)) }
        return ,$items.ToArray()
    }
    return $Value
}

function Assert-ModelPolicyObserved($Snapshot, $Binding) {
    if ($null -eq $Binding) { return $null }
    $observed = $Snapshot.creation.config.model_policy
    if ($null -eq $observed) { throw 'Inspected run config does not contain the requested model policy.' }
    $expectedJson = ConvertTo-Json -InputObject (ConvertTo-ComparableModelPolicyValue $Binding.Policy) -Depth 100 -Compress
    $observedJson = ConvertTo-Json -InputObject (ConvertTo-ComparableModelPolicyValue $observed) -Depth 100 -Compress
    if ($expectedJson -cne $observedJson) { throw 'Inspected embedded model policy does not match the requested policy file.' }
    $hasher = [Security.Cryptography.SHA256]::Create()
    try {
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        $digest = ([BitConverter]::ToString($hasher.ComputeHash($utf8.GetBytes($observedJson))) -replace '-', '').ToLowerInvariant()
    } finally { $hasher.Dispose() }
    return [ordered]@{ Present = $true; NormalizedPolicySha256 = $digest }
}

function Assert-WriterContractPreparedBinding($Prior, [string]$ExpectedContract) {
    $property = $Prior.PSObject.Properties['writer_contract_requested']
    $actual = if ($null -ne $property) { [string]$property.Value } else { '' }
    if ($actual -cne $ExpectedContract) { throw 'Writer contract must match the treatment recorded by the prepared run.' }
}

function Assert-WriterContractObserved($Snapshot, [string]$ExpectedContract) {
    if ($ExpectedContract -eq '') { return $null }
    $actual = [string]$Snapshot.creation.config.writer_contract
    if ($actual -cne $ExpectedContract) { throw "Inspected run writer contract '$actual' does not match requested '$ExpectedContract'." }
    return $actual
}

function Assert-FixerRouteObserved($Snapshot, [bool]$Enabled, [string]$ExpectedModel, [string]$ExpectedEffort) {
    if (-not $Enabled) { return $null }
    $fixerProfile = $Snapshot.creation.config.fixer
    if ($null -eq $fixerProfile -or [string]$fixerProfile.model -cne $ExpectedModel -or [string]$fixerProfile.effort -cne $ExpectedEffort) {
        throw 'Inspected run fixer route does not match the explicitly requested model and effort.'
    }
    return [ordered]@{ model = [string]$fixerProfile.model; effort = [string]$fixerProfile.effort }
}

function Get-IsolationPolicyBinding([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [IO.Path]::IsPathFullyQualified($Path) -or [IO.Path]::GetFullPath($Path) -cne $Path) {
        throw 'IsolationPolicyPath must be an absolute clean path passed unchanged.'
    }
    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'IsolationPolicyPath must name a regular non-reparse file.'
    }
    if ($item.Length -eq 0 -or $item.Length -gt (32 * 1024)) {
        throw 'Isolation policy must be nonempty and no larger than 32 KiB.'
    }
    $bytes = [IO.File]::ReadAllBytes($item.FullName)
    if ($bytes.Length -eq 0 -or $bytes.Length -gt (32 * 1024)) {
        throw 'Isolation policy must be nonempty and no larger than 32 KiB.'
    }
    $strictUtf8 = New-Object System.Text.UTF8Encoding($false, $true)
    $text = $strictUtf8.GetString($bytes)
    $document = $text | ConvertFrom-Json
    if ($document.version -ne 1 -or $null -eq $document.capacity -or $null -eq $document.estimate) {
        throw 'Isolation policy must contain version 1, capacity, and estimate objects.'
    }
    $hasher = [System.Security.Cryptography.SHA256]::Create()
    try { $sha256 = ([BitConverter]::ToString($hasher.ComputeHash($bytes)) -replace '-', '').ToLowerInvariant() }
    finally { $hasher.Dispose() }
    return [ordered]@{
        Path = $item.FullName
        Sha256 = $sha256
        Bytes = $bytes.Length
        Document = $document
    }
}

function Assert-CurrentIsolationPolicyBinding($Binding) {
    if ($null -eq $Binding) { throw 'Isolation policy binding is missing.' }
    $current = Get-IsolationPolicyBinding $Binding.Path
    if ($current.Path -cne $Binding.Path -or $current.Sha256 -cne $Binding.Sha256) {
        throw 'Isolation policy path or file bytes changed during this evaluation.'
    }
}

function Assert-IsolationPolicyObserved($Snapshot, $Binding, [int]$MaxParallel) {
    $execution = $Snapshot.creation.execution
    if ($null -eq $execution) { throw 'Inspected run does not include execution policy.' }
    $parallelVersion = if ($null -eq $execution.parallel_implementation_version) { 0 } else { [int]$execution.parallel_implementation_version }
    if ($execution.isolated_implementation_version -ne 1 -or
        $parallelVersion -ne 0 -or $execution.max_parallel -ne $MaxParallel) {
        throw 'Inspected run does not bind the requested isolated-writer scheduler policy.'
    }
    $expectedCapacity = $Binding.Document.capacity
    $expectedEstimate = $Binding.Document.estimate
    $observedCapacity = $execution.isolation_capacity
    $observedEstimate = $execution.isolation_estimate
    if ($null -eq $observedCapacity -or $null -eq $observedEstimate -or
        $observedCapacity.cpu_milli -ne $expectedCapacity.cpu_milli -or
        $observedCapacity.memory_mib -ne $expectedCapacity.memory_mib -or
        $observedCapacity.verification_slots -ne $expectedCapacity.verification_slots -or
        $observedCapacity.total_runtime_slots -ne $expectedCapacity.total_runtime_slots -or
        $observedEstimate.cpu_milli -ne $expectedEstimate.cpu_milli -or
        $observedEstimate.memory_mib -ne $expectedEstimate.memory_mib -or
        $observedEstimate.verification_slots -ne $expectedEstimate.verification_slots -or
        $observedEstimate.runtime_slots -ne $expectedEstimate.runtime_slots) {
        throw 'Inspected run capacity or estimate differs from the explicit isolation policy.'
    }
    $writer = $Snapshot.creation.config.writer
    if ($null -eq $writer -or $observedCapacity.provider_slots.Count -ne 1 -or
        $observedCapacity.model_slots.Count -ne 1 -or $observedCapacity.runtime_slots.Count -ne 1 -or
        $observedCapacity.provider_slots[0].provider -cne $writer.provider -or
        $observedCapacity.provider_slots[0].slots -ne $expectedCapacity.provider_slots -or
        $observedCapacity.model_slots[0].model.provider -cne $writer.provider -or
        $observedCapacity.model_slots[0].model.model -cne $writer.model -or
        $observedCapacity.model_slots[0].slots -ne $expectedCapacity.model_slots -or
        [string]::IsNullOrWhiteSpace([string]$observedCapacity.runtime_slots[0].runtime.profile_id) -or
        $observedCapacity.runtime_slots[0].runtime.provider -cne $writer.provider -or
        $observedCapacity.runtime_slots[0].runtime.model -cne $writer.model -or
        $observedCapacity.runtime_slots[0].slots -ne $expectedCapacity.runtime_slots) {
        throw 'Inspected isolated capacity is not bound to exactly the configured writer route.'
    }
    return [ordered]@{
        isolated_implementation_version = $execution.isolated_implementation_version
        max_parallel = $execution.max_parallel
        isolation_capacity = $observedCapacity
        isolation_estimate = $observedEstimate
    }
}

function Assert-IsolationPolicyPreparedBinding($Prior, $Binding, [bool]$Enabled, [int]$MaxParallel) {
    $preparedEnabled = [bool]$Prior.isolated_writers_requested
    if ($preparedEnabled -ne $Enabled) {
        throw 'IsolatedWriters must match the treatment recorded by the prepared run.'
    }
    if ($Enabled) {
        if ($null -eq $Binding -or
            [string]$Prior.isolation_policy_path_requested -cne $Binding.Path -or
            [string]$Prior.isolation_policy_sha256_requested -cne $Binding.Sha256 -or
            [int]$Prior.isolation_policy_version_requested -ne 1 -or
            [int]$Prior.max_parallel_requested -ne $MaxParallel) {
            throw 'Isolation policy path/hash, version and MaxParallel must match the explicit prepared treatment.'
        }
    } elseif ($null -ne $Prior.isolation_policy_sha256_requested -and $Prior.isolation_policy_sha256_requested -ne '') {
        throw 'Prepared run unexpectedly contains isolated-writer policy provenance.'
    }
}

function Assert-ReviewImpactPreparedBinding($Prior, [bool]$Enabled) {
    $preparedEnabled = $false
    if ($null -ne $Prior.PSObject.Properties['review_impact_context_requested']) {
        $preparedEnabled = [bool]$Prior.review_impact_context_requested
    }
    if ($preparedEnabled -ne $Enabled) {
        throw 'ReviewImpactContext must match the treatment recorded by the prepared run.'
    }
}

function Assert-CandidateFactsCachePreparedBinding($Prior, [bool]$Enabled) {
    $preparedVersion = 0
    if ($null -ne $Prior.PSObject.Properties['candidate_facts_cache_version_requested'] -and $null -ne $Prior.candidate_facts_cache_version_requested) {
        $preparedVersion = [int]$Prior.candidate_facts_cache_version_requested
    }
    $expectedVersion = if ($Enabled) { 1 } else { 0 }
    if ($preparedVersion -ne $expectedVersion) {
        throw 'CandidateFactsCache must match the versioned cache policy recorded by the prepared run.'
    }
}

function Assert-CandidateFactsCacheObserved($Snapshot, [bool]$Enabled) {
    $execution = $Snapshot.creation.execution
    $observedVersion = 0
    if ($null -ne $execution.candidate_facts_cache_version) { $observedVersion = [int]$execution.candidate_facts_cache_version }
    if (-not $Enabled) {
        if ($observedVersion -ne 0) { throw 'Inspected run unexpectedly enables candidate facts caching.' }
        return $null
    }
    if ($observedVersion -ne 1 -or [int]$execution.review_impact_context_version -ne 1) {
        throw 'Inspected run candidate facts cache policy does not match the requested review-impact treatment.'
    }
    return $observedVersion
}

function Assert-ReviewImpactObserved($Snapshot, [string]$ExpectedCandidateId, [bool]$Enabled) {
    $execution = $Snapshot.creation.execution
    $observedVersion = 0
    if ($null -ne $execution.review_impact_context_version) { $observedVersion = [int]$execution.review_impact_context_version }
    if (-not $Enabled) {
        if ($observedVersion -ne 0) { throw 'Inspected run unexpectedly enables reviewer impact context.' }
        return $null
    }
    if ($observedVersion -ne 1) { throw 'Inspected run review_impact_context_version does not match the requested treatment.' }
    $impactRecords = @($Snapshot.review_impact_contexts | Where-Object { [string]$_.candidate_id -ceq $ExpectedCandidateId })
    if ($impactRecords.Count -ne 1 -or $impactRecords[0].version -ne 1 -or
        [string]$impactRecords[0].candidate_files_hash -cne [string]$Snapshot.candidate.files_hash -or
        [string]$impactRecords[0].record_id -cnotmatch '^[0-9a-f]{64}$') {
        throw 'Inspected run lacks one valid review-impact record for the exact reviewed candidate.'
    }
    return [ordered]@{
        version = 1
        candidate_id = [string]$impactRecords[0].candidate_id
        record_id = [string]$impactRecords[0].record_id
        unavailable_reason = if ($null -ne $impactRecords[0].unavailable_reason) { [string]$impactRecords[0].unavailable_reason } else { $null }
        changed_path_count = [int]$impactRecords[0].changed_path_count
        admitted_path_count = [int]$impactRecords[0].admitted_path_count
        deleted_path_count = [int]$impactRecords[0].deleted_path_count
        omitted_count = [int]$impactRecords[0].omitted_count
    }
}

if (Test-GoSourceContextMode $PlannerContext) {
    if (-not (Test-Path -LiteralPath $PlannerContextRIExecutable -PathType Leaf)) {
        throw 'PlannerContextRIExecutable must name an existing regular file.'
    }
    $actualPlannerContextRIExecutableSHA256 = (Get-FileSha256 $PlannerContextRIExecutable).ToLowerInvariant()
    if ($actualPlannerContextRIExecutableSHA256 -cne $PlannerContextRIExecutableSHA256) {
        throw 'PlannerContextRIExecutableSHA256 does not match the selected parser executable bytes.'
    }
}

$isolationPolicyBinding = $null
if ($IsolatedWriters) {
    $isolationPolicyBinding = Get-IsolationPolicyBinding $IsolationPolicyPath
}

function Get-PinnedGoEnvironmentBinding([string]$GoPath) {
    $selected = (Resolve-Path -LiteralPath $GoPath -ErrorAction Stop).Path
    return [ordered]@{
        selected_go_path = $selected
        selected_go_sha256 = (Get-FileSha256 $selected).ToLowerInvariant()
        child_go_resolution = 'verified exact selected executable before each Fabric invocation'
        child_path_scope = 'selected Go directory prepended temporarily; caller PATH restored in finally'
    }
}

function Invoke-WithPinnedGo([string]$GoPath, [scriptblock]$Action) {
    if ($null -eq $Action) { throw 'Pinned-Go action is required.' }
    $selected = (Resolve-Path -LiteralPath $GoPath -ErrorAction Stop).Path
    $selectedDir = [System.IO.Path]::GetDirectoryName($selected)
    $previousPath = $env:PATH
    try {
        if ([string]::IsNullOrEmpty($previousPath)) {
            $env:PATH = $selectedDir
        } else {
            $env:PATH = $selectedDir + [System.IO.Path]::PathSeparator + $previousPath
        }

        $visibleGo = Get-Command go -CommandType Application -ErrorAction Stop
        $visiblePath = (Resolve-Path -LiteralPath $visibleGo.Source -ErrorAction Stop).Path
        if (-not [string]::Equals($visiblePath, $selected, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Child PATH did not resolve go to the selected executable.'
        }

        & $Action
    } finally {
        if ($null -eq $previousPath) {
            Remove-Item -Path 'env:PATH' -ErrorAction SilentlyContinue
        } else {
            $env:PATH = $previousPath
        }
    }
}

function Write-CandidateCopySnapshotProjection([object]$Snapshot, [string]$FullSnapshotPath, [string]$ProjectionPath) {
    # Keep the original complete inspect JSON for evaluation evidence and gate
    # decisions. The copy helper needs only these unchanged typed fields;
    # unrelated host journals can otherwise exceed its canonical decoder cap.
    $projection = [ordered]@{}
    foreach ($name in @('run_id', 'state', 'workspace', 'candidate', 'verification', 'review')) {
        $property = $Snapshot.PSObject.Properties[$name]
        if ($null -eq $property) { $projection[$name] = $null }
        else { $projection[$name] = $property.Value }
    }
    $json = ConvertTo-Json -InputObject $projection -Depth 100 -Compress
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($ProjectionPath, $json, $encoding)
    return [ordered]@{
        FullSnapshotSha256 = (Get-FileSha256 $FullSnapshotPath).ToLowerInvariant()
        ProjectionSha256 = (Get-FileSha256 $ProjectionPath).ToLowerInvariant()
        ProjectionBytes = [System.Text.Encoding]::UTF8.GetByteCount($json)
    }
}

function Get-HeldoutSource([string]$Check) {
    $map = @{
        'humanize'   = 'humanize.heldout_test.go'
        'commaf-performance' = 'commaf_performance.heldout_test.go'
        'afero'      = 'afero.heldout_test.go'
        'multierror' = 'multierror.heldout_test.go'
        'atomic'     = 'atomic.heldout_test.go'
        'atomic-numeric' = 'atomic_numeric.heldout_test.go'
        'difflib'    = 'difflib.heldout_test.go'
        'logr'       = 'logr.heldout_test.go'
        'godotenv'   = 'godotenv.heldout_test.go'
        'wordwrap-tabs' = 'wordwrap_tabs.heldout_test.go'
    }
    if (-not $map.ContainsKey($Check)) { throw "Unknown held-out check: $Check" }
    $p = Join-Path $heldoutDir $map[$Check]
    if (-not (Test-Path -LiteralPath $p)) { throw "Held-out fixture missing: $p" }
    return Get-Content -Raw -LiteralPath $p
}

function Get-HeldoutSources([string]$Check) {
    if ($Check -ne 'humanize-feature-performance') {
        $relativePath = if ($Check -eq 'difflib') { 'difflib/fabric_v1_heldout_test.go' } else { 'fabric_v1_heldout_test.go' }
        return @([pscustomobject]@{ RelativePath = $relativePath; Content = (Get-HeldoutSource $Check) })
    }

    # Preserve each existing oracle byte-for-byte on disk. Rename only their
    # package-level test entry points in the temporary candidate copy, then
    # use one exact-selector wrapper that requires both tests to run.
    $humanize = Get-HeldoutSource 'humanize'
    $commaf = Get-HeldoutSource 'commaf-performance'
    $testPattern = 'func TestFabricV1Heldout\(t \*testing\.T\)'
    if ([regex]::Matches($humanize, $testPattern).Count -ne 1 -or
        [regex]::Matches($commaf, $testPattern).Count -ne 1) {
        throw 'Combined humanize composition requires one exact held-out entry point in each source fixture.'
    }
    $humanize = [regex]::Replace($humanize, $testPattern, 'func runFabricV1HeldoutHumanize(t *testing.T)')
    $commaf = [regex]::Replace($commaf, $testPattern, 'func runFabricV1HeldoutCommaf(t *testing.T)')
    $wrapper = @'
package humanize

import "testing"

func TestFabricV1Heldout(t *testing.T) {
	t.Run("ParseBytesUnderscores", runFabricV1HeldoutHumanize)
	t.Run("CommafPerformance", runFabricV1HeldoutCommaf)
}
'@
    return @(
        [pscustomobject]@{ RelativePath = 'fabric_v1_heldout_humanize_test.go'; Content = $humanize }
        [pscustomobject]@{ RelativePath = 'fabric_v1_heldout_commaf_test.go'; Content = $commaf }
        [pscustomobject]@{ RelativePath = 'fabric_v1_heldout_test.go'; Content = $wrapper }
    )
}

function Write-HeldoutSources([string]$Root, [object[]]$Sources) {
    if ($Sources.Count -eq 0) { throw 'Held-out source set is empty.' }
    $rootFull = [System.IO.Path]::GetFullPath($Root).TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    $written = New-Object System.Collections.Generic.List[string]
    try {
        foreach ($source in $Sources) {
            $relative = [string]$source.RelativePath
            if ([System.IO.Path]::IsPathRooted($relative)) { throw 'Held-out source path must be relative.' }
            $destination = [System.IO.Path]::GetFullPath((Join-Path $rootFull $relative))
            if (-not $destination.StartsWith($rootFull, [StringComparison]::OrdinalIgnoreCase)) { throw 'Held-out source path escaped its disposable root.' }
            if (Test-Path -LiteralPath $destination) { throw "Held-out source destination already exists: $relative" }
            $parent = [System.IO.Path]::GetDirectoryName($destination)
            if (-not (Test-Path -LiteralPath $parent -PathType Container)) { throw "Held-out source parent is missing: $relative" }
            [System.IO.File]::WriteAllText($destination, [string]$source.Content, (New-Object System.Text.UTF8Encoding($false)))
            $written.Add($destination)
        }
        return ,$written.ToArray()
    } catch {
        foreach ($path in $written) { Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue }
        throw
    }
}

function Remove-HeldoutSources([string[]]$Paths) {
    foreach ($path in $Paths) {
        if (Test-Path -LiteralPath $path -PathType Leaf) { Remove-Item -LiteralPath $path -Force }
    }
}

function Get-NativeGoArgs([object]$Entry) {
    # Manifest native_argv policy; Windows-only skip of precisely
    # ^TestNocmpIntegration$ for go-atomic (cmd.Env HOME-only -> C:\Windows
    # temp). Linux runs the full native suite. Held-out tests never excluded.
    if ($null -eq $Entry.native_argv -or @($Entry.native_argv).Count -eq 0) { throw "Manifest native_argv policy missing for $($Entry.id)" }
    $testArgs = @($Entry.native_argv)
    $scope = 'full'
    if ($Entry.id -eq 'go-difflib') { $scope = 'legacy-tests-without-vet' }
    if (($env:OS -eq 'Windows_NT') -and ($Entry.id -in @('go-atomic', 'go-atomic-numeric-text'))) {
        $testArgs += @('-skip', '^TestNocmpIntegration$')
        $scope = 'windows-scoped'
    }
    return [ordered]@{ Argv = $testArgs; Scope = $scope }
}

function Get-HeldoutGoArgs([object]$Entry, [string]$HeldoutFile) {
    # Held-out invocation: same manifest policy base with the Fabric held-out
    # selector. Legacy go-difflib (no go.mod) appends the held-out file to the
    # explicit source-file list; all other repos keep the package pattern.
    if ($null -eq $Entry.native_argv -or @($Entry.native_argv).Count -eq 0) { throw "Manifest native_argv policy missing for $($Entry.id)" }
    if ($Entry.id -eq 'go-difflib') {
        return @(Add-FabricV1HeldoutArguments -FullArgv @($Entry.native_argv) -HeldoutFile $HeldoutFile)
    }
    return @(Add-FabricV1HeldoutArguments -FullArgv @($Entry.native_argv))
}

function Invoke-GoTest([string]$WorkDir, [string[]]$TestArgs) {
    # $TestArgs is the full manifest policy argv starting with `go`; strip
    # and validate the executable here so local runs never become `go go`.
    # Recorded policy argv (logs, run.json, harness.toml) keeps the full form.
    $split = Split-GoInvocation -FullArgv $TestArgs
    $outFile = [System.IO.Path]::GetTempFileName()
    $errFile = [System.IO.Path]::GetTempFileName()
    try {
        Push-Location -LiteralPath $WorkDir
        try {
            & $GoExe @($split.Args) 1> $outFile 2> $errFile
            $exit = $LASTEXITCODE
        } finally { Pop-Location }
        $stdout = Get-Content -Raw -LiteralPath $outFile
        if ($null -eq $stdout) { $stdout = '' }
        $stderr = Get-Content -Raw -LiteralPath $errFile
        if ($null -eq $stderr) { $stderr = '' }
        return [pscustomobject]@{ ExitCode = $exit; Stdout = $stdout; Stderr = $stderr; Combined = ($stdout + "`n" + $stderr) }
    } finally {
        Remove-Item -LiteralPath $outFile, $errFile -Force -ErrorAction SilentlyContinue
    }
}

function Set-TaskVerificationConfig([string]$TaskPath, [object]$Entry, [string]$ControllerStateRoot = '') {
    # init defaults required checks to `go test ./...`; the task policy
    # requires the explicit manifest native_argv BEFORE any run is created
    # (Creation.Config binds required checks). Rewrites and hashes harness.toml.
    $native = Get-NativeGoArgs $Entry
    $configPath = Join-Path $TaskPath 'harness.toml'
    if (-not (Test-Path -LiteralPath $configPath)) { throw "harness.toml missing after init for $($Entry.id)" }
    $text = Get-Content -Raw -LiteralPath $configPath
    # init emits single-quoted argv; rewritten policy uses double quotes.
    # Read-VerificationArgv supports both simple quote forms and rejects
    # malformed, extra, or empty material explicitly.
    $currentItems = @(Read-VerificationArgv -Text $text)
    $argvDrift = Compare-Object $currentItems @('go', 'test', './...')
    if ($null -ne $argvDrift) {
        throw "harness.toml verification argv is not the init default for $($entry.id): $($currentItems -join ' ')"
    }
    $argvMatch = [regex]::Match($text, '(?m)^argv\s*=\s*\[[^\]]*\]')
    if (-not $argvMatch.Success) { throw "no verification argv line in harness.toml for $($entry.id); refusing to guess config shape" }
    $tomlArgv = 'argv = [' + (($native.Argv | ForEach-Object { '"' + ($_ -replace '"', '\"') + '"' }) -join ', ') + ']'
    $text = $text.Substring(0, $argvMatch.Index) + $tomlArgv + $text.Substring($argvMatch.Index + $argvMatch.Length)
    if (-not [string]::IsNullOrWhiteSpace($ControllerStateRoot)) {
        $repositoryRoot = [IO.Path]::GetFullPath($TaskPath)
        $stateRoot = [IO.Path]::GetFullPath($ControllerStateRoot)
        if (-not [IO.Path]::IsPathRooted($ControllerStateRoot) -or $stateRoot -cne $ControllerStateRoot) {
            throw 'Isolated controller state root must be an absolute clean path.'
        }
        $within = {
            param([string]$Parent, [string]$Child)
            $relative = [IO.Path]::GetRelativePath($Parent, $Child)
            return $relative -eq '.' -or (-not [IO.Path]::IsPathRooted($relative) -and $relative -ne '..' -and -not $relative.StartsWith('..' + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -and -not $relative.StartsWith('..' + [IO.Path]::AltDirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase))
        }
        $stateWithinRepository = & $within $repositoryRoot $stateRoot
        $repositoryWithinState = & $within $stateRoot $repositoryRoot
        if ($stateWithinRepository -or $repositoryWithinState) {
            throw 'Isolated controller state root must be separate from the task checkout.'
        }
        $quotedStateRoot = ConvertTo-Json -InputObject $stateRoot -Compress
        $stateRootLines = [regex]::Matches($text, '(?m)^[ \t]*controller_state_root\b[^\n]*$')
        if ($stateRootLines.Count -gt 1) {
            throw 'Isolated task config contains duplicate controller_state_root settings.'
        }
        if ($stateRootLines.Count -eq 1) {
            # `fabric init` emits this default empty TOML string. Replace only
            # that exact empty setting; malformed or operator-supplied values
            # remain an error and the config file is not written on failure.
            $emptyStateRoot = [regex]::Match($text, '(?m)^(?<prefix>[ \t]*controller_state_root[ \t]*=[ \t]*)(?<value>(?<quote>[\x22\x27])\k<quote>)[ \t]*(?:\x23[^\r\n]*)?\r?$')
            if (-not $emptyStateRoot.Success) {
                throw 'Isolated task config controller_state_root must be a single empty TOML string before binding.'
            }
            $value = $emptyStateRoot.Groups['value']
            $text = $text.Substring(0, $value.Index) + $quotedStateRoot + $text.Substring($value.Index + $value.Length)
        } else {
            $text = 'controller_state_root = ' + $quotedStateRoot + "`n" + $text
        }
    }
    Set-Content -NoNewline -Encoding utf8 -LiteralPath $configPath $text
    return [ordered]@{
        Argv        = @($native.Argv)
        ArgvText    = ($native.Argv -join ' ')
        Scope       = $native.Scope
        ControllerStateRoot = if ([string]::IsNullOrWhiteSpace($ControllerStateRoot)) { $null } else { $stateRoot }
        ConfigSha   = (Get-FileSha256 $configPath).ToLowerInvariant()
    }
}

function Get-UsageMetrics([object]$Usage) {
    # internal/control RunUsage: invocations[].usage.provider_usage.
    # {input_tokens,output_tokens} (runtime.Usage *int64, null when
    # unavailable). receipt_matched rows are completed runtime invocations,
    # NOT a provider call count: provider_calls stays null (no actual count
    # exists in the contracts). Unknown metrics stay null, never zero.
    $inSum = 0; $outSum = 0; $inSeen = $false; $outSeen = $false
    $cachedSum = 0; $reasoningSum = 0; $typedSeen = 0
    $total = $null; $matched = $null
    if ($null -ne $Usage -and $null -ne $Usage.invocations) {
        $total = 0; $matched = 0
        foreach ($inv in @($Usage.invocations)) {
            $total++
            if ($inv.receipt_matched -eq $true) { $matched++ }
            $pu = $inv.usage.provider_usage
            if ($null -ne $pu) {
                if ($null -ne $pu.input_tokens) { $inSum += [int64]$pu.input_tokens; $inSeen = $true }
                if ($null -ne $pu.output_tokens) { $outSum += [int64]$pu.output_tokens; $outSeen = $true }
                $delta = $pu.accounting.delta
                if ($pu.accounting.coverage -eq 'OBSERVED' -and
                    $null -ne $pu.input_tokens -and $null -ne $pu.output_tokens -and
                    $null -ne $delta.inputTokens -and $null -ne $delta.outputTokens -and
                    $null -ne $delta.cachedInputTokens -and $null -ne $delta.reasoningOutputTokens -and
                    $delta.inputTokens -eq $pu.input_tokens -and $delta.outputTokens -eq $pu.output_tokens -and
                    $delta.cachedInputTokens -ge 0 -and $delta.cachedInputTokens -le $delta.inputTokens -and
                    $delta.reasoningOutputTokens -ge 0 -and $delta.reasoningOutputTokens -le $delta.outputTokens) {
                    $cachedSum += [int64]$delta.cachedInputTokens
                    $reasoningSum += [int64]$delta.reasoningOutputTokens
                    $typedSeen++
                }
            }
        }
    }
    return [ordered]@{
        RuntimeInvocations          = $total
        RuntimeInvocationsCompleted = $matched
        ProviderCalls               = $null
        InputTokens                 = if ($inSeen) { $inSum } else { $null }
        OutputTokens                = if ($outSeen) { $outSum } else { $null }
        CachedInputTokens           = if ($total -gt 0 -and $typedSeen -eq $total -and $matched -eq $total) { $cachedSum } else { $null }
        UncachedInputTokens         = if ($total -gt 0 -and $typedSeen -eq $total -and $matched -eq $total) { $inSum - $cachedSum } else { $null }
        ReasoningOutputTokens       = if ($total -gt 0 -and $typedSeen -eq $total -and $matched -eq $total) { $reasoningSum } else { $null }
        TokenTypeCoverage           = if ($total -gt 0 -and $typedSeen -eq $total -and $matched -eq $total) { 'observed' } elseif ($typedSeen -gt 0) { 'partial' } else { 'unknown' }
    }
}

function Get-CandidateDiffMetrics([string]$Workspace, [string]$OutDir) {
    # Safe git diff collection from the candidate path (PR5 baseline lacks
    # `diff`). --no-optional-locks plus -c diff.autoRefreshIndex=false keeps
    # read-only observation from mutating the index hash the candidate
    # binding depends on. Exact limit: 1 MiB total like internal/cli diff.go.
    # Unknown metrics stay null, never zero. Untracked added files counted
    # separately.
    $noLock = @('--no-optional-locks', '-c', 'diff.autoRefreshIndex=false')
    $metrics = [ordered]@{
        diff_files = $null; diff_insertions = $null; diff_deletions = $null
        untracked_added_files = $null; diff_bytes = $null
        diff_byte_limit = $script:DiffByteLimit; diff_truncated = $null; diff_note = $null
        index_lock_avoidance = 'git --no-optional-locks -c diff.autoRefreshIndex=false'
    }
    try {
        $numstat = & $git @noLock -C $Workspace diff --no-ext-diff --numstat HEAD --
        if ($LASTEXITCODE -ne 0) { $metrics.diff_note = 'git diff numstat failed'; return $metrics }
        $files = 0; $ins = 0; $del = 0; $bytes = 0
        foreach ($line in ($numstat -join "`n" -split "`n")) {
            $t = $line.Trim()
            if ($t -eq '') { continue }
            $m = [regex]::Match($t, '^(\d+)\s+(\d+)\s+\S')
            if ($m.Success) { $files++; $ins += [int]$m.Groups[1].Value; $del += [int]$m.Groups[2].Value }
            elseif ($t -match '^-\s+-\s+\S') { $files++ }
            else { $metrics.diff_note = "unparsable numstat line: $t"; return $metrics }
            $bytes += [System.Text.Encoding]::UTF8.GetByteCount($t) + 1
        }
        $untracked = & $git @noLock -C $Workspace ls-files --others --exclude-standard -z
        if ($LASTEXITCODE -ne 0) { $metrics.diff_note = 'git untracked listing failed'; return $metrics }
        $uCount = @($untracked -split "`0" | Where-Object { $_ -ne '' }).Count
        $metrics.diff_files = $files; $metrics.diff_insertions = $ins; $metrics.diff_deletions = $del
        $metrics.untracked_added_files = $uCount; $metrics.diff_bytes = $bytes
        $metrics.diff_truncated = ($bytes -gt $script:DiffByteLimit)
        if ($bytes -gt $script:DiffByteLimit) { $metrics.diff_note = "diff exceeds $($script:DiffByteLimit)-byte limit" }
        Set-Content -NoNewline -Encoding utf8 (Join-Path $OutDir 'candidate-numstat.log') ($numstat -join "`n")
        return $metrics
    } catch {
        $metrics.diff_note = "diff collection error: $($_.Exception.Message)"
        return $metrics
    }
}

function Get-BinaryGoProvenance([string]$BinaryPath) {
    # Exact binary bytes (SHA-256) plus embedded Go build metadata from
    # `GoExe version -m`. Source HEAD (runner tree) is recorded separately;
    # the embedded vcs.revision/vcs.modified is the binary's own build claim,
    # never blindly trusted as the runner source.
    $sha = (Get-FileSha256 $BinaryPath).ToLowerInvariant()
    $raw = (& $GoExe version -m $BinaryPath 2>&1 | Out-String)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect embedded Go build metadata.' }
    $rev = $null; $mod = $null
    $m = [regex]::Match($raw, 'vcs\.revision=([0-9a-f]{40})')
    if ($m.Success) { $rev = $m.Groups[1].Value.ToLowerInvariant() }
    $m2 = [regex]::Match($raw, 'vcs\.modified=(true|false)')
    if ($m2.Success) { $mod = $m2.Groups[1].Value.ToLowerInvariant() }
    return [ordered]@{ Sha256 = $sha; VersionM = $raw.Trim(); VcsRevision = $rev; VcsModified = $mod }
}

function Get-RunnerSourceHashes() {
    # Hash ALL runner/helper source inputs + manifest and heldout assets, not
    # just the primary PS script. Relative repo-rooted paths -> sha256.
    $rel = @(
        'scripts/evaluate-v1.ps1',
        'evals/v1/harness/Classify-CheckOutput.ps1',
        'evals/v1/harness/Copy-CandidateTree.ps1',
        'evals/v1/harness/Go-ArgvPolicy.ps1',
        'evals/v1/harness/Toml-ArgvPolicy.ps1',
        'evals/v1/harness/Test-PinnedGoEnvironment.ps1',
        'evals/v1/harness/Test-ArgvPolicy.ps1',
        'evals/v1/harness/Test-CopyFixtures.ps1',
        'evals/v1/harness/Test-TomlArgvPolicy.ps1',
        'evals/v1/harness/Test-NativeRunArgs.ps1',
        'evals/v1/harness/Test-AgentContextTreatment.ps1',
        'evals/v1/harness/Test-PublicObjectiveContract.ps1',
        'evals/v1/harness/Test-UsageMetrics.ps1',
        'evals/v1/manifest.json'
    )
    $out = [ordered]@{}
    foreach ($r in $rel) {
        $p = Join-Path $repoRoot ($r -replace '/', [string][System.IO.Path]::DirectorySeparatorChar)
        if (Test-Path -LiteralPath $p -PathType Leaf) { $out[$r] = (Get-FileSha256 $p).ToLowerInvariant() }
    }
    foreach ($f in @(Get-ChildItem -LiteralPath (Join-Path $repoRoot 'evals\v1\candidatecopy') -Filter '*.go' -File -ErrorAction SilentlyContinue)) {
        $out["evals/v1/candidatecopy/$($f.Name)"] = (Get-FileSha256 $f.FullName).ToLowerInvariant()
    }
    foreach ($f in @(Get-ChildItem -LiteralPath $heldoutDir -Filter '*.go' -File -ErrorAction SilentlyContinue)) {
        $out["evals/v1/heldout/$($f.Name)"] = (Get-FileSha256 $f.FullName).ToLowerInvariant()
    }
    return $out
}

function Get-ReviewCandidateId([object]$Snap) {
    if ($null -eq $Snap -or $null -eq $Snap.review -or $null -eq $Snap.review.result -or [string]::IsNullOrWhiteSpace($Snap.review.result.output)) { return $null }
    try { $v = ($Snap.review.result.output | ConvertFrom-Json) } catch { return $null }
    if ([string]::IsNullOrWhiteSpace($v.candidate_id)) { return $null }
    return [string]$v.candidate_id
}

function Invoke-CandidateCopy([string]$HelperExe, [string]$SnapshotFile, [string]$ExpectedCandidate, [string]$Destination, [string]$LogDir, [string]$LogPrefix = 'candidatecopy') {
    # Calls the explicit helper exe (fingerprinted by the caller, never
    # auto-built or relaunched) with the six-field copy projection. The full
    # inspect artifact remains untouched for gates and evidence. Helper
    # failure is BLOCKED before any acceptance tests, never rerun. A failed
    # destination is retained.
    $outLog = Join-Path $LogDir "$LogPrefix.stdout.log"
    $errLog = Join-Path $LogDir "$LogPrefix.stderr.log"
    & $HelperExe --snapshot $SnapshotFile --expected-candidate $ExpectedCandidate --destination $Destination 1> $outLog 2> $errLog
    $exit = $LASTEXITCODE
    $stdout = ''
    if (Test-Path -LiteralPath $outLog) { $stdout = Get-Content -Raw -LiteralPath $outLog }
    if ($null -eq $stdout) { $stdout = '' }
    if ($exit -ne 0) {
        $stderr = ''
        if (Test-Path -LiteralPath $errLog) { $stderr = Get-Content -Raw -LiteralPath $errLog }
        throw "candidatecopy helper BLOCKED (exit $exit): $($stderr.Trim())"
    }
    if ([string]::IsNullOrWhiteSpace($stdout)) { throw 'candidatecopy helper produced no output' }
    try { $parsed = ($stdout.Trim() | ConvertFrom-Json) } catch { throw "candidatecopy output is not JSON: $($_.Exception.Message)" }
    if ([string]::IsNullOrWhiteSpace($parsed.candidate_id) -or $parsed.candidate_id -ne $ExpectedCandidate) {
        throw 'candidatecopy observation candidate_id does not match expected reviewed candidate'
    }
    return $parsed
}

function Get-RunGate([object]$Snap, [string]$FabricRunId, [string]$DiffCandidateId, [string]$ExpectedCandidateId) {
    # Strict PASS gate from exact Snapshot/Verification/review contracts plus
    # the helper-observed candidate binding. Candidate.ID() itself is computed
    # inside the Go helper (canonical hash); here the expected reviewed digest
    # must appear in verification plan, every observation, and the review
    # verdict (and in the native diff when present). PR5 has no fabric diff
    # (diffCandidateId nil) and relies on the helper binding alone.
    # Returns @{ Pass; FailReason; BlockedReason; ... }. Missing/uncertain
    # evidence is BLOCKED, never rerun. Complete negative evidence is FAIL:
    # FAIL observations / failed review / REPAIRING state are FAIL, while
    # unknown/pending statuses and non-READY incomplete states are BLOCKED.
    $gate = [ordered]@{
        Pass = $false; FailReason = $null; BlockedReason = $null
        State = $null; RepairAttempts = $null
        ReviewDecision = $null; ReviewFindings = $null
        ReviewCandidateId = $null; ReviewPlanId = $null
        VerificationObservations = $null; VerificationChecks = $null
        ExpectedCandidateId = $ExpectedCandidateId
    }
    if ($null -eq $Snap) { $gate.BlockedReason = 'inspect produced no snapshot'; return $gate }
    $gate.State = $Snap.state
    $gate.RepairAttempts = $Snap.repair_attempts
    if ([string]::IsNullOrWhiteSpace($ExpectedCandidateId) -or $ExpectedCandidateId -notmatch '^[0-9a-f]{64}$') { $gate.BlockedReason = 'missing expected reviewed candidate binding'; return $gate }
    if ($Snap.run_id -ne $FabricRunId) { $gate.BlockedReason = 'snapshot run_id binding mismatch'; return $gate }
    if ($null -eq $Snap.candidate) { $gate.BlockedReason = 'missing candidate observation'; return $gate }
    if ($null -eq $Snap.verification) { $gate.BlockedReason = 'missing verification state'; return $gate }
    if ($Snap.verification.pending -eq $true) { $gate.BlockedReason = 'verification pending'; return $gate }
    if ($Snap.verification.plan.candidate_id -ne $ExpectedCandidateId) { $gate.BlockedReason = 'verification plan candidate does not match expected reviewed candidate'; return $gate }
    $obs = @($Snap.verification.observations)
    $inv = @($Snap.verification.plan.invocations)
    $gate.VerificationObservations = $obs.Count
    $gate.VerificationChecks = $inv.Count
    if ($inv.Count -eq 0 -or $obs.Count -ne $inv.Count) { $gate.BlockedReason = 'verification observations do not cover all planned checks'; return $gate }
    foreach ($o in $obs) {
        if ($o.result.candidate_id -ne $ExpectedCandidateId -or $o.result.candidate_id -ne $Snap.verification.plan.candidate_id) { $gate.BlockedReason = 'verification result candidate binding mismatch'; return $gate }
        $st = [string]$o.result.status
        if ($st -eq 'PASS') { continue }
        elseif ($st -eq 'FAIL') { $gate.FailReason = 'verification observation not PASS'; return $gate }
        else { $gate.BlockedReason = "verification observation status is $st, not PASS"; return $gate }
    }
    if ($null -eq $Snap.review) { $gate.BlockedReason = 'missing review record'; return $gate }
    try { $verdict = ($Snap.review.result.output | ConvertFrom-Json) }
    catch { $gate.BlockedReason = "review output is not a JSON verdict: $($_.Exception.Message)"; return $gate }
    $gate.ReviewDecision = $verdict.decision
    $gate.ReviewFindings = @($verdict.findings).Count
    $gate.ReviewCandidateId = $verdict.candidate_id
    $gate.ReviewPlanId = $verdict.verification_plan_id
    if ($verdict.candidate_id -ne $ExpectedCandidateId) { $gate.BlockedReason = 'review candidate_id does not match expected reviewed candidate'; return $gate }
    if ($verdict.candidate_id -ne $Snap.verification.plan.candidate_id) { $gate.BlockedReason = 'review candidate_id does not match verification candidate'; return $gate }
    if ($verdict.verification_plan_id -ne $Snap.verification.plan_id) { $gate.BlockedReason = 'review verification_plan_id does not match verification plan'; return $gate }
    if ($null -ne $DiffCandidateId -and $DiffCandidateId -ne '' -and $DiffCandidateId -ne $verdict.candidate_id) {
        $gate.BlockedReason = 'diff candidate_id does not match reviewed candidate'; return $gate
    }
    if ($verdict.decision -ne 'approve' -or @($verdict.findings).Count -ne 0) { $gate.FailReason = 'reviewer did not approve with zero findings'; return $gate }
    if ($Snap.state -eq 'READY') { $gate.Pass = $true; return $gate }
    elseif ($Snap.state -eq 'REPAIRING') { $gate.FailReason = "snapshot state is REPAIRING after complete negative evidence"; return $gate }
    else { $gate.BlockedReason = "snapshot state is $($Snap.state), not READY"; return $gate }
}

$GoExe = (Resolve-Path -LiteralPath $GoExe -ErrorAction Stop).Path
if (-not (Test-Path -LiteralPath $GoExe -PathType Leaf)) { throw 'Pinned Go executable is not a file.' }
$fixerAccessBinding = if ($AccessConfigExplicit) { Get-AccessConfigBinding $AccessConfigPath } else { $null }
$modelPolicyBinding = if ($ModelPolicyExplicit) { Get-ModelPolicyBinding $ModelPolicyPath } else { $null }
$goVersion = (& $GoExe version).Trim()
if ($LASTEXITCODE -ne 0 -or $goVersion -notmatch 'go1\.27\.1') { throw "Expected Go 1.27.1, got $goVersion" }
$goEnvironmentBinding = Get-PinnedGoEnvironmentBinding $GoExe
$productHead = Get-GitText $repoRoot @('rev-parse', 'HEAD')
$productDirty = [bool]((Get-GitText $repoRoot @('status', '--porcelain')) -ne '')
$runnerSha = (Get-FileSha256 $PSCommandPath).ToLowerInvariant()

$entries = @($manifest.repositories)
if ($TaskIds -and $TaskIds.Count -gt 0) {
    $wanted = @($TaskIds)
    $entries = @($entries | Where-Object { $wanted -contains $_.id })
    if ($entries.Count -ne $wanted.Count) { throw 'Unknown -TaskIds selection.' }
}

if ($Action -eq 'Prepare') {
    # Prepare MUST NOT invoke any provider: no FabricExe/CodexExe run, no model
    # calls, no run-task script. Only git + go test discriminator.
    $now = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ')
    if (-not $RunId) { $RunId = "$now-$([Guid]::NewGuid().ToString('N').Substring(0, 8))" }
    if ($RunId -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { throw 'RunId must contain only letters, digits, dot, underscore, or dash.' }
    $runPath = Join-Path (Join-Path $RunRoot $manifest.suite_id) $RunId
    if (Test-Path $runPath) { throw "Run directory already exists (new run nonce required): $runPath" }
    $taskRoot = Join-Path $runPath 'tasks'
    $baselineRoot = Join-Path $runPath 'baseline-checks'
    New-Item -ItemType Directory -Force $taskRoot, $baselineRoot | Out-Null
    $records = @()
    $started = [Diagnostics.Stopwatch]::StartNew()
    foreach ($entry in $entries) {
        $taskPath = Join-Path (Join-Path $taskRoot $entry.id) 'repo'
        $basePath = Join-Path $baselineRoot $entry.id
        New-Item -ItemType Directory -Force (Split-Path $taskPath), $basePath | Out-Null
        & $git clone -q --no-checkout --filter=blob:none $entry.url $taskPath
        if ($LASTEXITCODE -ne 0) { throw "Clone failed for $($entry.id)" }
        & $git -C $taskPath checkout -q --detach $entry.sha
        if ($LASTEXITCODE -ne 0) { throw "Pinned checkout failed for $($entry.id)" }
        $actual = Get-GitText $taskPath @('rev-parse', 'HEAD')
        if ($actual -ne $entry.sha) { throw "SHA mismatch for $($entry.id): $actual" }
        $excludePath = Join-Path $taskPath '.git\info\exclude'
        Add-Content -LiteralPath $excludePath -Value "`n.harness/`nharness.toml`n" -Encoding utf8
        if ((Get-GitText $taskPath @('status', '--porcelain')) -ne '') { throw "Task checkout dirty after exclude for $($entry.id)" }
        & $git clone -q --no-hardlinks --local $taskPath $basePath
        if ($LASTEXITCODE -ne 0) { throw "Baseline copy failed for $($entry.id)" }
        $baseHead = Get-GitText $basePath @('rev-parse', 'HEAD')

        $native = Get-NativeGoArgs $entry
        $preflight = Invoke-GoTest $basePath $native.Argv
        Set-Content -NoNewline -Encoding utf8 (Join-Path $basePath 'preflight.stdout.log') $preflight.Stdout
        Set-Content -NoNewline -Encoding utf8 (Join-Path $basePath 'preflight.stderr.log') $preflight.Stderr
        Set-Content -NoNewline -Encoding utf8 (Join-Path $basePath 'preflight.args.log') ($native.Argv -join ' ')
        Set-Content -NoNewline -Encoding utf8 (Join-Path $basePath 'preflight.scope.log') $native.Scope
        if ($preflight.ExitCode -ne 0) {
            throw "$($entry.id) preflight FAILED on pinned baseline (exit $($preflight.ExitCode), scope $($native.Scope), args: $($native.Argv -join ' ')); see baseline-checks/$($entry.id)/preflight.*.log"
        }

        if ($entry.check -eq 'difflib') {
            $heldoutArg = 'difflib/fabric_v1_heldout_test.go'
            $heldoutSources = Get-HeldoutSources $entry.check
        } else {
            $heldoutSources = Get-HeldoutSources $entry.check
            $heldoutArg = ''
        }
        foreach ($source in $heldoutSources) {
            if (Test-Path -LiteralPath (Join-Path $taskPath $source.RelativePath)) { throw "Held-out test leaked into task checkout for $($entry.id)" }
        }
        $writtenHeldoutSources = Write-HeldoutSources $basePath $heldoutSources
        try {
            $heldout = Invoke-GoTest $basePath (Get-HeldoutGoArgs $entry $heldoutArg)
        } finally { Remove-HeldoutSources $writtenHeldoutSources }
        Set-Content -NoNewline -Encoding utf8 (Join-Path $basePath 'heldout.stdout.log') $heldout.Stdout
        Set-Content -NoNewline -Encoding utf8 (Join-Path $basePath 'heldout.stderr.log') $heldout.Stderr
        Set-Content -NoNewline -Encoding utf8 (Join-Path $basePath 'heldout.combined.log') $heldout.Combined
        $classification = Get-FabricV1CheckClassification -TaskId $entry.id -ExitCode $heldout.ExitCode -CombinedOutput $heldout.Combined
        if ($classification -notin @('targeted_assertion', 'expected_missing_api')) {
            throw "$($entry.id) held-out baseline classification=$classification (exit $($heldout.ExitCode)); expected targeted_assertion or expected_missing_api."
        }
        if ($classification -eq 'expected_missing_api' -and $entry.id -notin @('go-multierror', 'go-atomic')) {
            throw "$($entry.id) missing-API baseline only expected for go-multierror ErrorsSnapshot and go-atomic MarshalText/UnmarshalText."
        }
        $records += [ordered]@{
            task_id                = $entry.id
            repository_url         = $entry.url
            source_sha             = $actual
            baseline_copy_sha      = $baseHead
            license                = $entry.license
            task_checkout          = $taskPath
            heldout_baseline       = 'FAIL_EXPECTED'
            heldout_classification = $classification
            heldout_exit_code      = $heldout.ExitCode
            preflight_exit_code    = $preflight.ExitCode
            preflight_args         = ($native.Argv -join ' ')
            native_argv_policy     = (@($entry.native_argv) -join ' ')
            native_test_scope      = $native.Scope
            windows_exclusion      = if ($native.Scope -eq 'windows-scoped') { '^TestNocmpIntegration$ (Windows-only atomic tasks; rationale in manifest native_verification)' } else { $null }
            task_completion        = 'NOT RUN'
            planner_context_requested = $PlannerContext
            agent_context_requested = $AgentContext
            planner_context_ri_executable_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutable } else { $null }
            planner_context_ri_executable_sha256_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutableSHA256 } else { $null }
            prompt_recipe_requested = $PromptRecipe
            auto_compact_token_limit_requested = if ($AutoCompactTokenLimit -gt 0) { $AutoCompactTokenLimit } else { $null }
            provider_calls         = 0
            input_tokens           = $null
            output_tokens          = $null
            review_findings        = $null
            repair_attempts        = $null
            diff_files             = $null
            diff_insertions        = $null
            diff_deletions         = $null
            untracked_added_files  = $null
            human_interventions    = $null
            elapsed_ms             = $null
            candidate_sha          = $null
            candidate_tree_sha256  = $null
        }
        if ($RepairIntelligence) { $records[-1].repair_intelligence_version_requested = 1 }
        if ($ReviewImpactContext) { $records[-1].review_impact_context_requested = $true }
        if ($CandidateFactsCache) { $records[-1].candidate_facts_cache_version_requested = 1 }
        if ($FixerModelExplicit) { $records[-1].fixer_model_requested = $FixerModel }
        if ($FixerEffortExplicit) { $records[-1].fixer_effort_requested = $FixerEffort }
        if ($null -ne $fixerAccessBinding) {
            $records[-1].access_config_path_requested = $fixerAccessBinding.Path
            $records[-1].access_config_sha256_requested = $fixerAccessBinding.Sha256
            $records[-1].access_config_bytes_requested = $fixerAccessBinding.Bytes
        }
        if ($null -ne $modelPolicyBinding) {
            $records[-1].model_policy_path_requested = $modelPolicyBinding.Path
            $records[-1].model_policy_sha256_requested = $modelPolicyBinding.Sha256
            $records[-1].model_policy_bytes_requested = $modelPolicyBinding.Bytes
        }
        if ($writerContractRequested -ne '') { $records[-1].writer_contract_requested = $writerContractRequested }
        if ($IsolatedWriters) {
            $records[-1].isolated_writers_requested = $true
            $records[-1].max_parallel_requested = $MaxParallel
            $records[-1].isolation_policy_version_requested = 1
            $records[-1].isolation_policy_path_requested = $isolationPolicyBinding.Path
            $records[-1].isolation_policy_sha256_requested = $isolationPolicyBinding.Sha256
            $records[-1].isolation_policy_bytes_requested = $isolationPolicyBinding.Bytes
        }
    }
    $started.Stop()
    $record = [ordered]@{
        schema_version                  = 1
        suite_id                      = $manifest.suite_id
        run_id                        = $RunId
        mode                          = 'prepare'
        planner_context_requested  = $PlannerContext
        agent_context_requested = $AgentContext
        planner_context_ri_executable_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutable } else { $null }
        planner_context_ri_executable_sha256_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutableSHA256 } else { $null }
        planner_context_ri_executable_sha256_verified = if (Test-GoSourceContextMode $PlannerContext) { $actualPlannerContextRIExecutableSHA256 } else { $null }
        prompt_recipe_requested  = $PromptRecipe
        auto_compact_token_limit_requested = if ($AutoCompactTokenLimit -gt 0) { $AutoCompactTokenLimit } else { $null }
        created_utc                   = [DateTime]::UtcNow.ToString('o')
        product_head                  = $productHead
        product_tree_dirty_at_prepare = $productDirty
        runner_sha256                 = $runnerSha
        go_version                    = $goVersion
        go_environment_binding        = $goEnvironmentBinding
        preparation_elapsed_ms        = $started.ElapsedMilliseconds
        provider_calls                = 0
        heldout_checks                = 'baseline discriminator only; task checkouts never contained held-out tests; exact stdout/stderr under baseline-checks/<id>/*.log'
        native_verification           = $manifest.native_verification
        native_test_scope_note        = 'Windows verification is scoped, not the full upstream suite: Windows-only -skip ^TestNocmpIntegration$ applies to go-atomic preflight and final native package verification; Linux runs the full native suite; legacy go-difflib uses explicit source-file argv; held-out task tests are never excluded.'
        raw_transcripts_retained      = $false
        credentials_retained          = $false
        task_records                  = $records
    }
    if ($RepairIntelligence) { $record.repair_intelligence_version_requested = 1 }
    if ($ReviewImpactContext) { $record.review_impact_context_requested = $true }
    if ($CandidateFactsCache) { $record.candidate_facts_cache_version_requested = 1 }
    if ($FixerModelExplicit) { $record.fixer_model_requested = $FixerModel }
    if ($FixerEffortExplicit) { $record.fixer_effort_requested = $FixerEffort }
    if ($null -ne $fixerAccessBinding) {
        $record.access_config_path_requested = $fixerAccessBinding.Path
        $record.access_config_sha256_requested = $fixerAccessBinding.Sha256
        $record.access_config_bytes_requested = $fixerAccessBinding.Bytes
    }
    if ($null -ne $modelPolicyBinding) {
        $record.model_policy_path_requested = $modelPolicyBinding.Path
        $record.model_policy_sha256_requested = $modelPolicyBinding.Sha256
        $record.model_policy_bytes_requested = $modelPolicyBinding.Bytes
    }
    if ($writerContractRequested -ne '') { $record.writer_contract_requested = $writerContractRequested }
    if ($IsolatedWriters) {
        $record.isolated_writers_requested = $true
        $record.max_parallel_requested = $MaxParallel
        $record.isolation_policy_version_requested = 1
        $record.isolation_policy_path_requested = $isolationPolicyBinding.Path
        $record.isolation_policy_sha256_requested = $isolationPolicyBinding.Sha256
        $record.isolation_policy_bytes_requested = $isolationPolicyBinding.Bytes
    }
    $record | ConvertTo-Json -Depth 8 | Set-Content -Encoding utf8 (Join-Path $runPath 'run.json')
    Write-Output "PASS prepared $($records.Count) pinned tasks; provider calls=0"
    Write-Output "Run record: $(Join-Path $runPath 'run.json')"
    # The held-out discriminator intentionally fails on the unmodified base.
    # Do not leak that expected native exit code as the successful runner exit.
    exit 0
}

# ---------------- Evaluate (provider-gated) ----------------
if ([string]::IsNullOrWhiteSpace($FabricExe)) { throw '-FabricExe is required for Evaluate.' }
if ([string]::IsNullOrWhiteSpace($CodexExe)) { throw '-CodexExe is required for Evaluate.' }
if ([string]::IsNullOrWhiteSpace($Model)) { throw '-Model is required for Evaluate.' }
if ([string]::IsNullOrWhiteSpace($Effort)) { throw '-Effort must not be empty; default high, user-requested value accepted and persisted.' }
if (-not $RunId) { throw '-RunId of a prepared run is required for Evaluate.' }
if ($RunId -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { throw 'RunId must contain only letters, digits, dot, underscore, or dash (same bound as Prepare).' }
if ([string]::IsNullOrWhiteSpace($CandidateCopyExe)) { throw '-CandidateCopyExe is required for Evaluate (explicit helper path, fingerprinted, never auto-built).' }
$FabricExe = (Resolve-Path -LiteralPath $FabricExe).Path
$CodexExe = (Resolve-Path -LiteralPath $CodexExe).Path
$CandidateCopyExe = (Resolve-Path -LiteralPath $CandidateCopyExe).Path
$fabricProv = Get-BinaryGoProvenance $FabricExe
if ($EvalMode -eq 'Native' -and ($null -eq $fabricProv.VcsRevision -or $fabricProv.VcsModified -ne 'false')) {
    throw 'Native evaluation requires a binary with an embedded commit and clean VCS metadata.'
}
$fabricSha = $fabricProv.Sha256
$copyProv = Get-BinaryGoProvenance $CandidateCopyExe
$copySha = $copyProv.Sha256
$runtimeSha = (Get-FileSha256 $CodexExe).ToLowerInvariant()
if ($EvalMode -eq 'PR5Matched') {
    if ([string]::IsNullOrWhiteSpace($PR5BaselineExe)) { throw '-PR5BaselineExe is required for PR5Matched mode.' }
    $baselineExe = (Resolve-Path -LiteralPath $PR5BaselineExe).Path
    $baselineProv = Get-BinaryGoProvenance $baselineExe
    $baselineSha = $baselineProv.Sha256
    $pr5Script = if ([string]::IsNullOrWhiteSpace($PR5BaselineScript)) { $defaultRunTaskScript } else { (Resolve-Path -LiteralPath $PR5BaselineScript).Path }
    if (-not (Test-Path -LiteralPath $pr5Script)) { throw "PR5 baseline script not found: $pr5Script" }
    $pr5ScriptSha = (Get-FileSha256 $pr5Script).ToLowerInvariant()
}
# Evaluation-time provenance: current runner HEAD/dirty is the runner tree
# (which is dirty due to untracked helper source), NOT the clean binary's
# build source. Binary vcs.revision/vcs.modified above is the binary's own
# embedded claim. Do not claim clean binary source from a dirty runner HEAD.
$evalProductHead = Get-GitText $repoRoot @('rev-parse', 'HEAD')
$evalProductDirty = [bool]((Get-GitText $repoRoot @('status', '--porcelain')) -ne '')
$runnerSourceHashes = Get-RunnerSourceHashes
$runPath = Join-Path (Join-Path $RunRoot $manifest.suite_id) $RunId
$runJsonPath = Join-Path $runPath 'run.json'
if (-not (Test-Path -LiteralPath $runJsonPath)) { throw "Prepared run not found: $runJsonPath. Prepare first with a new run nonce." }
$priorRunJsonSha = (Get-FileSha256 $runJsonPath).ToLowerInvariant()
$prior = Get-Content -Raw -LiteralPath $runJsonPath | ConvertFrom-Json
Assert-AgentContextPreparedBinding $prior $AgentContext
Assert-RepairIntelligencePreparedBinding $prior ([bool]$RepairIntelligence)
Assert-AutoCompactPreparedBinding $prior $AutoCompactTokenLimit
Assert-ReviewImpactPreparedBinding $prior ([bool]$ReviewImpactContext)
Assert-CandidateFactsCachePreparedBinding $prior ([bool]$CandidateFactsCache)
Assert-FixerAccessPreparedBinding $prior $FixerModelExplicit $FixerModel $FixerEffortExplicit $FixerEffort $fixerAccessBinding
Assert-ModelPolicyPreparedBinding $prior $ModelPolicyExplicit $modelPolicyBinding
Assert-WriterContractPreparedBinding $prior $writerContractRequested
$preparedPlannerContext = [string]$prior.planner_context_requested
if ($preparedPlannerContext -ne $PlannerContext) {
    throw 'PlannerContext must match the treatment recorded by the prepared run.'
}
$preparedPlannerContextRIExecutable = [string]$prior.planner_context_ri_executable_requested
$preparedPlannerContextRIExecutableSHA256 = [string]$prior.planner_context_ri_executable_sha256_requested
if ($preparedPlannerContextRIExecutable -cne $PlannerContextRIExecutable -or
    $preparedPlannerContextRIExecutableSHA256 -cne $PlannerContextRIExecutableSHA256) {
    throw 'PlannerContext RI parser path and hash must match the explicit treatment recorded by the prepared run.'
}
$preparedPromptRecipe = [string]$prior.prompt_recipe_requested
if ($preparedPromptRecipe -ne $PromptRecipe) {
    throw 'PromptRecipe must match the treatment recorded by the prepared run.'
}
Assert-IsolationPolicyPreparedBinding $prior $isolationPolicyBinding ([bool]$IsolatedWriters) $MaxParallel
$taskPins = @($entries | ForEach-Object { [ordered]@{ task_id = $_.id; source_sha = $_.sha; url = $_.url } })
# Optional known external build receipt: validate binary hash when present,
# never blindly trust stamped source.
$buildReceipt = $null
if (-not [string]::IsNullOrWhiteSpace($NativeBuildReceiptPath)) {
    $buildReceiptPath = (Resolve-Path -LiteralPath $NativeBuildReceiptPath -ErrorAction Stop).Path
    try {
        $receiptRaw = Get-Content -Raw -LiteralPath $buildReceiptPath
        $receiptJson = ($receiptRaw | ConvertFrom-Json)
        $receiptHash = $null
        foreach ($k in @('binary_sha256', 'fabric_sha256', 'sha256', 'binary_hash')) {
            if ($null -ne $receiptJson.$k -and $receiptJson.$k -ne '') { $receiptHash = [string]$receiptJson.$k; break }
        }
        $buildReceipt = [ordered]@{
            path = $buildReceiptPath
            receipt_sha256 = (Get-FileSha256 $buildReceiptPath).ToLowerInvariant()
            claimed_binary_sha256 = $receiptHash
            matches_native_binary = if ($null -ne $receiptHash) { ($receiptHash.ToLowerInvariant() -eq $fabricSha) } else { $null }
        }
        if ($null -ne $receiptHash -and $receiptHash.ToLowerInvariant() -ne $fabricSha) {
            throw "known build receipt binary hash does not match Native FabricExe bytes; refusing to trust stamped source"
        }
        if ($EvalMode -eq 'Native' -and ($receiptJson.source_commit -ne $fabricProv.VcsRevision -or
            $receiptJson.source_clean_before -ne $true -or $receiptJson.source_clean_after -ne $true)) {
            throw 'Known native build receipt disagrees with embedded source identity or clean build evidence.'
        }
    } catch {
        throw "build receipt validation failed: $($_.Exception.Message)"
    }
}
$nowEval = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ')
$evalId = "eval-$nowEval-$([Guid]::NewGuid().ToString('N').Substring(0, 8))"
$evalRoot = Join-Path $runPath $evalId
if (Test-Path -LiteralPath $evalRoot) { throw "Eval directory already exists (new eval nonce required): $evalRoot" }
New-Item -ItemType Directory -Path $evalRoot | Out-Null
$results = @()
foreach ($entry in $entries) {
    $taskPath = Join-Path (Join-Path (Join-Path $runPath 'tasks') $entry.id) 'repo'
    $taskOutDir = Join-Path $evalRoot $entry.id
    New-Item -ItemType Directory -Force $taskOutDir | Out-Null
    $sw = [Diagnostics.Stopwatch]::StartNew()
    $result = [ordered]@{
        task_id = $entry.id; eval_mode = $EvalMode; terminal_state = 'BLOCKED'
        blocked_reason = $null; fail_reason = $null
        planner_context_requested = $PlannerContext
        agent_context_requested = $AgentContext
        planner_context_ri_executable_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutable } else { $null }
        planner_context_ri_executable_sha256_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutableSHA256 } else { $null }
        prompt_recipe_requested = $PromptRecipe
        auto_compact_token_limit_requested = if ($AutoCompactTokenLimit -gt 0) { $AutoCompactTokenLimit } else { $null }
    }
    if ($RepairIntelligence) { $result.repair_intelligence_version_requested = 1 }
    if ($ReviewImpactContext) { $result.review_impact_context_requested = $true }
    if ($CandidateFactsCache) { $result.candidate_facts_cache_version_requested = 1 }
    if ($FixerModelExplicit) { $result.fixer_model_requested = $FixerModel }
    if ($FixerEffortExplicit) { $result.fixer_effort_requested = $FixerEffort }
    if ($null -ne $fixerAccessBinding) {
        $result.access_config_path_requested = $fixerAccessBinding.Path
        $result.access_config_sha256_requested = $fixerAccessBinding.Sha256
        $result.access_config_bytes_requested = $fixerAccessBinding.Bytes
    }
    if ($null -ne $modelPolicyBinding) {
        $result.model_policy_path_requested = $modelPolicyBinding.Path
        $result.model_policy_sha256_requested = $modelPolicyBinding.Sha256
        $result.model_policy_bytes_requested = $modelPolicyBinding.Bytes
    }
    if ($writerContractRequested -ne '') { $result.writer_contract_requested = $writerContractRequested }
    if ($IsolatedWriters) {
        $result.isolated_writers_requested = $true
        $result.max_parallel_requested = $MaxParallel
        $result.isolation_policy_version_requested = 1
        $result.isolation_policy_path_requested = $isolationPolicyBinding.Path
        $result.isolation_policy_sha256_requested = $isolationPolicyBinding.Sha256
        $result.isolation_policy_bytes_requested = $isolationPolicyBinding.Bytes
    }
    try {
        if (-not (Test-Path -LiteralPath $taskPath)) { throw "Task checkout missing for $($entry.id): $taskPath" }
        if ((Get-GitText $taskPath @('rev-parse', 'HEAD')) -ne $entry.sha) { throw "Task checkout is not the fresh pinned SHA for $($entry.id); matched comparison requires fresh identical repo/task clones" }
        foreach ($source in (Get-HeldoutSources $entry.check)) {
            if (Test-Path -LiteralPath (Join-Path $taskPath $source.RelativePath)) { throw "Held-out test present in task clone before candidate completion for $($entry.id)" }
        }
        $result.source_sha = $entry.sha

        if ($EvalMode -eq 'Native') {
            # Native new evaluation: explicit init, task-policy verification
            # config BEFORE run creation, autonomous run, inspect/usage/diff.
            if ((Test-Path -LiteralPath (Join-Path $taskPath 'harness.toml')) -or (Test-Path -LiteralPath (Join-Path $taskPath '.harness'))) {
                throw "Task checkout for $($entry.id) already initialized; Evaluate requires a fresh prepared clone"
            }
            if ($null -ne $fixerAccessBinding) { Assert-CurrentAccessConfigBinding $fixerAccessBinding }
            if ($null -ne $modelPolicyBinding) { Assert-CurrentModelPolicyBinding $modelPolicyBinding }
            $initArgs = @(Get-NativeInitArgs $taskPath $CodexExe $Model $Effort ([bool]$ValidateWriterEdits) $FixerModel $FixerEffort $(if ($null -ne $fixerAccessBinding) { $fixerAccessBinding.Path } else { '' }) ([bool]$StrictWriterEdits) $(if ($null -ne $modelPolicyBinding) { $modelPolicyBinding.Path } else { '' }))
            Invoke-WithPinnedGo $GoExe {
                & $FabricExe @initArgs 1> (Join-Path $taskOutDir 'fabric-init.stdout.log') 2> (Join-Path $taskOutDir 'fabric-init.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "fabric init failed for $($entry.id); see fabric-init.*.log" }
            }
            if ($null -ne $modelPolicyBinding) { Assert-CurrentModelPolicyBinding $modelPolicyBinding }
            $result.effort = $Effort
            $controllerStateRoot = if ($IsolatedWriters) { [IO.Path]::GetFullPath((Join-Path $taskOutDir 'controller-state')) } else { '' }
            $verPolicy = Set-TaskVerificationConfig $taskPath $entry $controllerStateRoot
            $result.verification_argv = $verPolicy.ArgvText
            $result.verification_config_sha256 = $verPolicy.ConfigSha
            $result.native_test_scope = $verPolicy.Scope
            Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'verification-argv.log') $verPolicy.ArgvText
            $runArgs = @(Get-NativeRunArgs $taskPath $entry.task ([bool]$ParallelWriters) $MaxParallel $PlannerContext $PromptRecipe $PlannerContextRIExecutable $PlannerContextRIExecutableSHA256 ([bool]$IsolatedWriters) $isolationPolicyBinding.Path $AutoCompactTokenLimit ([bool]$ReviewImpactContext) ([bool]$CandidateFactsCache) $AgentContext ([bool]$RepairIntelligence))
            $result.parallel_writers_requested = [bool]$ParallelWriters
            if ($IsolatedWriters) { $result.isolated_writers_requested = $true }
            if ($AutoCompactTokenLimit -gt 0) { $result.auto_compact_token_limit_requested = $AutoCompactTokenLimit }
            $result.writer_edit_validation_requested = [bool]$ValidateWriterEdits
            if ($writerContractRequested -ne '') { $result.writer_contract_requested = $writerContractRequested }
            $result.max_parallel_requested = if ($MaxParallel -eq 0) { $null } else { $MaxParallel }
            Invoke-WithPinnedGo $GoExe {
                if ($IsolatedWriters) { Assert-CurrentIsolationPolicyBinding $isolationPolicyBinding }
                & $FabricExe @runArgs 1> (Join-Path $taskOutDir 'fabric-run.stdout.log') 2> (Join-Path $taskOutDir 'fabric-run.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "fabric run --autonomous failed for $($entry.id); see fabric-run.*.log" }
                if ($IsolatedWriters) { Assert-CurrentIsolationPolicyBinding $isolationPolicyBinding }
            }
            $runOut = Get-Content -Raw -LiteralPath (Join-Path $taskOutDir 'fabric-run.stdout.log')
            if ([string]::IsNullOrWhiteSpace($runOut)) { throw "fabric run produced no output for $($entry.id)" }
            $parsed = ($runOut | ConvertFrom-Json)
            $fabricRunId = $parsed.run_id
            if ([string]::IsNullOrWhiteSpace($fabricRunId) -or $fabricRunId -notmatch '^[0-9a-f]{64}$') { throw "fabric run output has no exact run identity for $($entry.id)" }
            $result.fabric_run_id = $fabricRunId
            $result.fabric_sha256 = $fabricSha
            Invoke-WithPinnedGo $GoExe {
                & $FabricExe --root $taskPath inspect $fabricRunId 1> (Join-Path $taskOutDir 'fabric-inspect.stdout.log') 2> (Join-Path $taskOutDir 'fabric-inspect.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "fabric inspect failed for $($entry.id); see fabric-inspect.*.log" }
            }
            Invoke-WithPinnedGo $GoExe {
                & $FabricExe --root $taskPath usage $fabricRunId 1> (Join-Path $taskOutDir 'fabric-usage.stdout.log') 2> (Join-Path $taskOutDir 'fabric-usage.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "fabric usage failed for $($entry.id); see fabric-usage.*.log" }
            }
            Invoke-WithPinnedGo $GoExe {
                & $FabricExe --root $taskPath diff $fabricRunId 1> (Join-Path $taskOutDir 'fabric-diff.stdout.log') 2> (Join-Path $taskOutDir 'fabric-diff.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "fabric diff failed for $($entry.id); see fabric-diff.*.log" }
            }
            $snap = (Get-Content -Raw -LiteralPath (Join-Path $taskOutDir 'fabric-inspect.stdout.log') | ConvertFrom-Json)
            $observedPlannerContext = [string]$snap.creation.execution.planner_context
            $result.planner_context_observed = $observedPlannerContext
            if ($observedPlannerContext -ne $PlannerContext) { throw 'Inspected run planner_context does not match the requested treatment.' }
            $observedPlannerContextRIExecutable = [string]$snap.creation.execution.planner_context_ri_executable
            $observedPlannerContextRIExecutableSHA256 = [string]$snap.creation.execution.planner_context_ri_executable_sha256
            $result.planner_context_ri_executable_observed = if (Test-GoSourceContextMode $PlannerContext) { $observedPlannerContextRIExecutable } else { $null }
            $result.planner_context_ri_executable_sha256_observed = if (Test-GoSourceContextMode $PlannerContext) { $observedPlannerContextRIExecutableSHA256 } else { $null }
            if ($observedPlannerContextRIExecutable -cne $PlannerContextRIExecutable -or
                $observedPlannerContextRIExecutableSHA256 -cne $PlannerContextRIExecutableSHA256) {
                throw 'Inspected run RI parser path/hash does not match the requested treatment.'
            }
            $observedPromptRecipe = [string]$snap.creation.execution.prompt_recipe
            $result.prompt_recipe_observed = $observedPromptRecipe
            if ($observedPromptRecipe -ne $PromptRecipe) { throw 'Inspected run prompt_recipe does not match the requested treatment.' }
            $result.agent_context_observed = Assert-AgentContextObserved $snap $AgentContext
            $result.repair_intelligence_version_observed = Assert-RepairIntelligenceObserved $snap ([bool]$RepairIntelligence)
            $autoCompactObserved = Assert-AutoCompactObserved $snap $AutoCompactTokenLimit
            $result.auto_compact_token_limit_observed = if ($null -ne $autoCompactObserved) { $autoCompactObserved.token_limit } else { $null }
            $candidateFactsCacheObserved = Assert-CandidateFactsCacheObserved $snap ([bool]$CandidateFactsCache)
            if ($null -ne $candidateFactsCacheObserved) { $result.candidate_facts_cache_version_observed = $candidateFactsCacheObserved }
            $writerContractObserved = Assert-WriterContractObserved $snap $writerContractRequested
            if ($null -ne $writerContractObserved) { $result.writer_contract_observed = $writerContractObserved }
            $fixerRouteObserved = Assert-FixerRouteObserved $snap ($FixerModelExplicit -or $FixerEffortExplicit) $(if ($FixerModelExplicit) { $FixerModel } else { $Model }) $(if ($FixerEffortExplicit) { $FixerEffort } else { $Effort })
            if ($null -ne $fixerRouteObserved) { $result.fixer_route_config_observed = $fixerRouteObserved }
            $modelPolicyObserved = Assert-ModelPolicyObserved $snap $modelPolicyBinding
            if ($null -ne $modelPolicyObserved) { $result.model_policy_config_observed = $modelPolicyObserved }
            if ($IsolatedWriters) {
                $expectedStateRoot = [IO.Path]::GetFullPath((Join-Path $taskOutDir 'controller-state'))
                if ([string]$snap.creation.config.controller_state_root -cne $expectedStateRoot) {
                    throw 'Inspected isolated run controller_state_root does not match its external per-task evaluation path.'
                }
                $result.controller_state_root_configured = $true
                $isolationObserved = Assert-IsolationPolicyObserved $snap $isolationPolicyBinding $MaxParallel
                $result.isolated_implementation_version_observed = $isolationObserved.isolated_implementation_version
                $result.isolation_capacity_observed = $isolationObserved.isolation_capacity
                $result.isolation_estimate_observed = $isolationObserved.isolation_estimate
            }
            $usage = (Get-Content -Raw -LiteralPath (Join-Path $taskOutDir 'fabric-usage.stdout.log') | ConvertFrom-Json)
            $diffOut = (Get-Content -Raw -LiteralPath (Join-Path $taskOutDir 'fabric-diff.stdout.log') | ConvertFrom-Json)
            $diffCandidateId = $diffOut.candidate_id
            $result.fabric_diff_candidate_id = $diffCandidateId
            $result.diff_source = 'fabric-diff'
            # Native autonomous journey performs zero human interventions.
            $result.human_interventions = @()
            $result.model = $Model
            $result.runtime_sha256 = $runtimeSha
            $result.config_sha256 = (Get-FileSha256 (Join-Path $taskPath 'harness.toml')).ToLowerInvariant()
            $metrics = Get-UsageMetrics $usage
            $result.runtime_invocations = $metrics.RuntimeInvocations
            $result.runtime_invocations_completed = $metrics.RuntimeInvocationsCompleted
            $result.provider_calls = $metrics.ProviderCalls
            $result.input_tokens = $metrics.InputTokens
            $result.output_tokens = $metrics.OutputTokens
            $result.cached_input_tokens = $metrics.CachedInputTokens
            $result.uncached_input_tokens = $metrics.UncachedInputTokens
            $result.reasoning_output_tokens = $metrics.ReasoningOutputTokens
            $result.token_type_coverage = $metrics.TokenTypeCoverage
        } else {
            # PR5 matched evaluation: explicit supplied old script; init with
            # the baseline exe/model first, same verification policy and
            # model/runtime; inspect+usage with the baseline executable;
            # exact run identity from the script result (never the first
            # arbitrary hash); diff collected from the candidate path because
            # the baseline lacks a diff command.
            if ((Test-Path -LiteralPath (Join-Path $taskPath 'harness.toml')) -or (Test-Path -LiteralPath (Join-Path $taskPath '.harness'))) {
                throw "Task checkout for $($entry.id) already initialized; Evaluate requires a fresh prepared clone"
            }
            Invoke-WithPinnedGo $GoExe {
                & $baselineExe --root $taskPath init --codex $CodexExe --model $Model --effort $Effort 1> (Join-Path $taskOutDir 'baseline-init.stdout.log') 2> (Join-Path $taskOutDir 'baseline-init.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "baseline init failed for $($entry.id); see baseline-init.*.log" }
            }
            $result.effort = $Effort
            $verPolicy = Set-TaskVerificationConfig $taskPath $entry
            $result.verification_argv = $verPolicy.ArgvText
            $result.verification_config_sha256 = $verPolicy.ConfigSha
            $result.native_test_scope = $verPolicy.Scope
            Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'verification-argv.log') $verPolicy.ArgvText
            # Old run-task.ps1 calls git diff --stat itself; do not edit that
            # baseline script. Scope diff.autoRefreshIndex=false via
            # GIT_CONFIG_COUNT appends so its reads cannot mutate the index
            # hash. Pre-existing entries are preserved and the environment is
            # restored in finally even on failure.
            $savedGitConfigCount = $env:GIT_CONFIG_COUNT
            $appendIndex = 0
            if (-not [string]::IsNullOrEmpty($savedGitConfigCount)) {
                $parsedCount = 0
                if (-not [int]::TryParse($savedGitConfigCount, [ref]$parsedCount)) { throw "existing GIT_CONFIG_COUNT is not an integer: $savedGitConfigCount" }
                $appendIndex = $parsedCount
            }
            $scopedKey = "GIT_CONFIG_KEY_$appendIndex"
            $scopedValue = "GIT_CONFIG_VALUE_$appendIndex"
            if ((Test-Path "env:$scopedKey") -or (Test-Path "env:$scopedValue")) { throw "GIT_CONFIG slot collision at index $appendIndex" }
            try {
                $env:GIT_CONFIG_COUNT = ($appendIndex + 1).ToString()
                Set-Item -Path "env:$scopedKey" -Value 'diff.autoRefreshIndex'
                Set-Item -Path "env:$scopedValue" -Value 'false'
                $taskResult = Invoke-WithPinnedGo $GoExe {
                    & $pr5Script -Fabric $baselineExe -Repository $taskPath -Objective $entry.task -ApprovePlan -ApproveChanges -NonInteractive
                }
            } finally {
                Remove-Item -Path "env:$scopedKey" -ErrorAction SilentlyContinue
                Remove-Item -Path "env:$scopedValue" -ErrorAction SilentlyContinue
                if ($null -eq $savedGitConfigCount) { Remove-Item -Path 'env:GIT_CONFIG_COUNT' -ErrorAction SilentlyContinue }
                else { $env:GIT_CONFIG_COUNT = $savedGitConfigCount }
            }
            $result.pr5_git_config_scope = 'appended diff.autoRefreshIndex=false via GIT_CONFIG_COUNT; pre-existing entries preserved and restored in finally'
            $exactId = @($taskResult) | Where-Object { $null -ne $_ -and $_.run_id -match '^[0-9a-f]{64}$' } | Select-Object -Last 1
            if ($null -eq $exactId) {
                throw "PR5 script returned no exact run identity for $($entry.id)"
            }
            $fabricRunId = [string]$exactId.run_id
            $result.fabric_run_id = $fabricRunId
            $result.baseline_sha256 = $baselineSha
            $result.baseline_script = $pr5Script
            Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'run-task.result.log') ("run_id=$fabricRunId state=$($taskResult.state)")
            Invoke-WithPinnedGo $GoExe {
                & $baselineExe --root $taskPath inspect $fabricRunId 1> (Join-Path $taskOutDir 'fabric-inspect.stdout.log') 2> (Join-Path $taskOutDir 'fabric-inspect.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "baseline inspect failed for $($entry.id); see fabric-inspect.*.log" }
            }
            Invoke-WithPinnedGo $GoExe {
                & $baselineExe --root $taskPath usage $fabricRunId 1> (Join-Path $taskOutDir 'fabric-usage.stdout.log') 2> (Join-Path $taskOutDir 'fabric-usage.stderr.log')
                if ($LASTEXITCODE -ne 0) { throw "baseline usage failed for $($entry.id); see fabric-usage.*.log" }
            }
            $snap = (Get-Content -Raw -LiteralPath (Join-Path $taskOutDir 'fabric-inspect.stdout.log') | ConvertFrom-Json)
            $usage = (Get-Content -Raw -LiteralPath (Join-Path $taskOutDir 'fabric-usage.stdout.log') | ConvertFrom-Json)
            $diffCandidateId = $null
            $result.diff_source = 'candidate-path-git'
            # Exactly two required approval gates, preauthorized, no invented
            # human identity, no repair loop started by this runner.
            $result.human_interventions = @(
                [ordered]@{ gate = 'plan'; mechanism = 'run-task.ps1 -ApprovePlan'; actor = 'preauthorized'; human_identity = $null }
                [ordered]@{ gate = 'file'; mechanism = 'run-task.ps1 -ApproveChanges -NonInteractive'; actor = 'preauthorized'; human_identity = $null }
            )
            $result.model = $Model
            $result.runtime_sha256 = $runtimeSha
            $result.config_sha256 = (Get-FileSha256 (Join-Path $taskPath 'harness.toml')).ToLowerInvariant()
            $metrics = Get-UsageMetrics $usage
            $result.runtime_invocations = $metrics.RuntimeInvocations
            $result.runtime_invocations_completed = $metrics.RuntimeInvocationsCompleted
            $result.provider_calls = $metrics.ProviderCalls
            $result.input_tokens = $metrics.InputTokens
            $result.output_tokens = $metrics.OutputTokens
            $result.cached_input_tokens = $metrics.CachedInputTokens
            $result.uncached_input_tokens = $metrics.UncachedInputTokens
            $result.reasoning_output_tokens = $metrics.ReasoningOutputTokens
            $result.token_type_coverage = $metrics.TokenTypeCoverage
        }

        # Workspace: exact contract path snap.workspace.request.path, bound to
        # this run. No object-string coercion, no task-checkout fallback.
        $wsReq = $snap.workspace.request
        if ($null -eq $wsReq -or [string]::IsNullOrWhiteSpace($wsReq.path)) { throw "snapshot has no isolated workspace for $($entry.id)" }
        $candidateWorkspace = [string]$wsReq.path
        $expectedRoot = Join-Path $taskPath (Join-Path '.harness' 'worktrees')
        if ($wsReq.run_id -ne $fabricRunId) { throw "workspace request is not bound to run $fabricRunId for $($entry.id)" }
        if (-not $candidateWorkspace.StartsWith($expectedRoot + [string][System.IO.Path]::DirectorySeparatorChar)) {
            throw "isolated workspace is outside the task worktree registry for $($entry.id)"
        }
        if (-not (Test-Path -LiteralPath $candidateWorkspace -PathType Container)) { throw "isolated workspace path missing for $($entry.id)" }
        if ($snap.candidate.head -ne $entry.sha) { throw "candidate HEAD is not the pinned source commit for $($entry.id)" }
        $result.candidate_workspace = $candidateWorkspace

        # Reviewed candidate binding: the Go helper owns Candidate.ID()
        # computation (canonical hash). Extract the reviewed digest here and
        # require it in both the helper and Get-RunGate. Native diff digest
        # must still agree when present; PR5 has no fabric diff (nil).
        $expectedCandidateId = Get-ReviewCandidateId $snap
        if ([string]::IsNullOrWhiteSpace($expectedCandidateId)) {
            $result.blocked_reason = 'missing reviewed candidate binding for helper'
            $sw.Stop()
            $result.elapsed_ms = $sw.ElapsedMilliseconds
            $results += $result
            continue
        }
        $result.review_candidate_id_expected = $expectedCandidateId
        $reviewImpactObserved = Assert-ReviewImpactObserved $snap $expectedCandidateId ([bool]$ReviewImpactContext)
        if ($null -ne $reviewImpactObserved) {
            $result.review_impact_context_version_observed = $reviewImpactObserved.version
            $result.review_impact_context_candidate_id_observed = $reviewImpactObserved.candidate_id
            $result.review_impact_context_record_id_observed = $reviewImpactObserved.record_id
            $result.review_impact_context_unavailable_reason_observed = $reviewImpactObserved.unavailable_reason
            $result.review_impact_context_counts_observed = [ordered]@{
                changed = $reviewImpactObserved.changed_path_count
                admitted = $reviewImpactObserved.admitted_path_count
                deleted = $reviewImpactObserved.deleted_path_count
                omitted = $reviewImpactObserved.omitted_count
            }
        }

        # Candidate-bound observation/copy under a read lease. Helper failure
        # is BLOCKED before any acceptance tests, never rerun; a failed
        # destination is retained, never recursively deleted.
        $copyStage = Join-Path $taskOutDir 'acceptance-copy'
        $fullSnapshotPath = Join-Path $taskOutDir 'fabric-inspect.stdout.log'
        $copySnapshotPath = Join-Path $taskOutDir 'candidatecopy-snapshot.json'
        $copySnapshotEvidence = Write-CandidateCopySnapshotProjection $snap $fullSnapshotPath $copySnapshotPath
        $result.inspect_snapshot_sha256 = $copySnapshotEvidence.FullSnapshotSha256
        $result.candidatecopy_snapshot_projection_sha256 = $copySnapshotEvidence.ProjectionSha256
        $result.candidatecopy_snapshot_projection_bytes = $copySnapshotEvidence.ProjectionBytes
        try {
            $copyObs = Invoke-CandidateCopy $CandidateCopyExe $copySnapshotPath $expectedCandidateId $copyStage $taskOutDir
        } catch {
            $result.blocked_reason = "candidatecopy helper BLOCKED: $($_.Exception.Message)"
            $sw.Stop()
            $result.elapsed_ms = $sw.ElapsedMilliseconds
            $results += $result
            continue
        }
        $copyPath = [string]$copyObs.destination
        $result.acceptance_copy_path = $copyPath
        $result.acceptance_copy_identity = [string]$copyObs.files_hash
        $result.acceptance_copy_files = [int]$copyObs.file_count
        $result.acceptance_copy_candidate_id = [string]$copyObs.candidate_id
        $result.acceptance_copy_workspace = [string]$copyObs.workspace
        $result.native_copy_path = $copyPath
        $result.native_copy_identity = [string]$copyObs.files_hash
        $result.native_copy_files = [int]$copyObs.file_count
        $result.native_copy_candidate_id = [string]$copyObs.candidate_id
        $result.native_copy_workspace = [string]$copyObs.workspace
        $copyObs | ConvertTo-Json -Depth 6 | Set-Content -Encoding utf8 (Join-Path $taskOutDir 'acceptance-copy.manifest.json')
        if ([string]$copyObs.candidate_id -ne $expectedCandidateId) {
            $result.blocked_reason = 'helper observation candidate_id does not match expected reviewed candidate'
            $sw.Stop()
            $result.elapsed_ms = $sw.ElapsedMilliseconds
            $results += $result
            continue
        }

        $gate = Get-RunGate $snap $fabricRunId $diffCandidateId $expectedCandidateId
        $result.snapshot_state = $gate.State
        $result.repair_attempts = $gate.RepairAttempts
        $result.review_decision = $gate.ReviewDecision
        $result.review_findings = $gate.ReviewFindings
        $result.review_candidate_id = $gate.ReviewCandidateId
        $result.review_verification_plan_id = $gate.ReviewPlanId
        $result.verification_observations = $gate.VerificationObservations
        $result.verification_checks = $gate.VerificationChecks
        if ($null -ne $gate.BlockedReason) {
            # Missing/uncertain evidence: BLOCKED, never automatically rerun.
            $result.blocked_reason = $gate.BlockedReason
            $sw.Stop()
            $result.elapsed_ms = $sw.ElapsedMilliseconds
            $results += $result
            continue
        }

        # Native tests run on their own helper copy. They are candidate code
        # and may mutate that copy, so held-out acceptance gets a second fresh
        # helper copy from the unchanged candidate workspace after native tests
        # finish. Both copies must bind to the same reviewed candidate.
        $native = Get-NativeGoArgs $entry
        $nativeVerify = Invoke-GoTest $copyPath $native.Argv
        Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'native-verify.stdout.log') $nativeVerify.Stdout
        Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'native-verify.stderr.log') $nativeVerify.Stderr
        Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'native-verify.args.log') ($native.Argv -join ' ')
        $result.native_verify_exit_code = $nativeVerify.ExitCode
        $result.native_verify_args = ($native.Argv -join ' ')

        $heldoutCopyStage = Join-Path $taskOutDir 'heldout-acceptance-copy'
        try {
            $heldoutCopyObs = Invoke-CandidateCopy $CandidateCopyExe $copySnapshotPath $expectedCandidateId $heldoutCopyStage $taskOutDir 'heldout-candidatecopy'
        } catch {
            $result.blocked_reason = "heldout candidatecopy helper BLOCKED: $($_.Exception.Message)"
            $sw.Stop()
            $result.elapsed_ms = $sw.ElapsedMilliseconds
            $results += $result
            continue
        }
        $heldoutCopyPath = [string]$heldoutCopyObs.destination
        $result.heldout_copy_path = $heldoutCopyPath
        $result.heldout_copy_identity = [string]$heldoutCopyObs.files_hash
        $result.heldout_copy_files = [int]$heldoutCopyObs.file_count
        $result.heldout_copy_candidate_id = [string]$heldoutCopyObs.candidate_id
        $result.heldout_copy_workspace = [string]$heldoutCopyObs.workspace
        $heldoutCopyObs | ConvertTo-Json -Depth 6 | Set-Content -Encoding utf8 (Join-Path $taskOutDir 'heldout-acceptance-copy.manifest.json')
        if ([string]$heldoutCopyObs.candidate_id -ne $expectedCandidateId -or
            [string]$heldoutCopyObs.files_hash -ne [string]$copyObs.files_hash -or
            [string]$heldoutCopyObs.workspace -ne [string]$copyObs.workspace -or
            [int]$heldoutCopyObs.file_count -ne [int]$copyObs.file_count) {
            $result.blocked_reason = 'heldout helper copy does not match the native copy candidate binding'
            $sw.Stop()
            $result.elapsed_ms = $sw.ElapsedMilliseconds
            $results += $result
            continue
        }

        $heldoutSources = Get-HeldoutSources $entry.check
        $heldoutFileArg = if ($entry.check -eq 'difflib') { 'difflib/fabric_v1_heldout_test.go' } else { '' }
        $writtenHeldoutSources = Write-HeldoutSources $heldoutCopyPath $heldoutSources
        try {
            $heldout = Invoke-GoTest $heldoutCopyPath (Get-HeldoutGoArgs $entry $heldoutFileArg)
        } finally { Remove-HeldoutSources $writtenHeldoutSources }
        Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'acceptance.stdout.log') $heldout.Stdout
        Set-Content -NoNewline -Encoding utf8 (Join-Path $taskOutDir 'acceptance.stderr.log') $heldout.Stderr
        $acceptClass = Get-FabricV1CheckClassification -TaskId $entry.id -ExitCode $heldout.ExitCode -CombinedOutput $heldout.Combined
        $result.acceptance_classification = $acceptClass
        $result.acceptance_exit_code = $heldout.ExitCode
        if ($acceptClass -eq 'setup_error') {
            $result.blocked_reason = 'acceptance setup_error; environment failure is never an intended result'
            $sw.Stop()
            $result.elapsed_ms = $sw.ElapsedMilliseconds
            $results += $result
            continue
        }

        $diffMetrics = Get-CandidateDiffMetrics $candidateWorkspace $taskOutDir
        $result.diff_files = $diffMetrics.diff_files
        $result.diff_insertions = $diffMetrics.diff_insertions
        $result.diff_deletions = $diffMetrics.diff_deletions
        $result.untracked_added_files = $diffMetrics.untracked_added_files
        $result.diff_bytes = $diffMetrics.diff_bytes
        $result.diff_byte_limit = $diffMetrics.diff_byte_limit
        $result.diff_truncated = $diffMetrics.diff_truncated
        $result.diff_note = $diffMetrics.diff_note
        $result.index_lock_avoidance = $diffMetrics.index_lock_avoidance

        # PASS requires the Fabric gate AND native upstream verification AND
        # held-out PASS. Complete-but-negative evidence is FAIL; anything
        # missing/uncertain is BLOCKED above.
        if ($gate.Pass -and $nativeVerify.ExitCode -eq 0 -and $heldout.ExitCode -eq 0 -and $acceptClass -eq 'pass') {
            $result.terminal_state = 'PASS'
        } elseif ($nativeVerify.ExitCode -ne 0 -or $heldout.ExitCode -ne 0 -or $null -ne $gate.FailReason) {
            $result.terminal_state = 'FAIL'
            $result.fail_reason = if ($null -ne $gate.FailReason) { $gate.FailReason } elseif ($nativeVerify.ExitCode -ne 0) { 'native upstream verification failed' } else { 'held-out acceptance failed' }
        } else {
            $result.blocked_reason = 'undetermined outcome; treated as BLOCKED without rerun'
        }
        $sw.Stop()
        $result.elapsed_ms = $sw.ElapsedMilliseconds
        $results += $result
    } catch {
        # Exact error preserved; uncertain provider work is never rerun here.
        $result.blocked_reason = "evaluate error: $($_.Exception.Message)"
        try { $sw.Stop() } catch { }
        try { $result.elapsed_ms = $sw.ElapsedMilliseconds } catch { }
        $results += $result
    }
}
$evalRecord = [ordered]@{
    schema_version           = 1
    suite_id               = $prior.suite_id
    run_id                 = $prior.run_id
    eval_id                = $evalId
    eval_mode              = $EvalMode
    model                  = $Model
    effort                 = $Effort
    planner_context_requested = $PlannerContext
    agent_context_requested = $AgentContext
    planner_context_ri_executable_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutable } else { $null }
    planner_context_ri_executable_sha256_requested = if (Test-GoSourceContextMode $PlannerContext) { $PlannerContextRIExecutableSHA256 } else { $null }
    planner_context_ri_executable_sha256_verified = if (Test-GoSourceContextMode $PlannerContext) { $actualPlannerContextRIExecutableSHA256 } else { $null }
    prompt_recipe_requested = $PromptRecipe
    auto_compact_token_limit_requested = if ($AutoCompactTokenLimit -gt 0) { $AutoCompactTokenLimit } else { $null }
    parallel_writers_requested = [bool]$ParallelWriters
    writer_edit_validation_requested = [bool]$ValidateWriterEdits
    max_parallel_requested = if ($MaxParallel -eq 0) { $null } else { $MaxParallel }
    fabric_sha256          = if ($EvalMode -eq 'Native') { $fabricSha } else { $null }
    fabric_go_version_m    = if ($EvalMode -eq 'Native') { $fabricProv.VersionM } else { $null }
    fabric_vcs_revision    = if ($EvalMode -eq 'Native') { $fabricProv.VcsRevision } else { $null }
    fabric_vcs_modified    = if ($EvalMode -eq 'Native') { $fabricProv.VcsModified } else { $null }
    baseline_sha256        = if ($EvalMode -eq 'PR5Matched') { $baselineSha } else { $null }
    baseline_go_version_m  = if ($EvalMode -eq 'PR5Matched') { $baselineProv.VersionM } else { $null }
    baseline_vcs_revision  = if ($EvalMode -eq 'PR5Matched') { $baselineProv.VcsRevision } else { $null }
    baseline_vcs_modified  = if ($EvalMode -eq 'PR5Matched') { $baselineProv.VcsModified } else { $null }
    baseline_script        = if ($EvalMode -eq 'PR5Matched') { $pr5Script } else { $null }
    baseline_script_sha256 = if ($EvalMode -eq 'PR5Matched') { $pr5ScriptSha } else { $null }
    candidate_copy_exe     = $CandidateCopyExe
    candidate_copy_sha256  = $copySha
    candidate_copy_go_version_m = $copyProv.VersionM
    candidate_copy_vcs_revision = $copyProv.VcsRevision
    candidate_copy_vcs_modified = $copyProv.VcsModified
    runtime_sha256         = $runtimeSha
    runner_sha256          = $runnerSha
    runner_source_hashes   = $runnerSourceHashes
    product_head_at_evaluate = $evalProductHead
    product_tree_dirty_at_evaluate = $evalProductDirty
    product_head_note      = 'Runner HEAD/dirty is the evaluation-time runner tree (dirty due to untracked helper source); binary vcs.revision/vcs.modified above is the binary build claim, not the runner HEAD. Do not claim clean binary source from a dirty runner HEAD.'
    go_version             = $goVersion
    go_environment_binding = $goEnvironmentBinding
    prior_run_json_sha256  = $priorRunJsonSha
    task_pins              = $taskPins
    build_receipt          = $buildReceipt
    created_utc            = [DateTime]::UtcNow.ToString('o')
    native_verification    = $manifest.native_verification
    raw_transcripts_retained = $false
    credentials_retained   = $false
    results                = $results
}
if ($RepairIntelligence) { $evalRecord.repair_intelligence_version_requested = 1 }
if ($ReviewImpactContext) { $evalRecord.review_impact_context_requested = $true }
if ($CandidateFactsCache) { $evalRecord.candidate_facts_cache_version_requested = 1 }
if ($FixerModelExplicit) { $evalRecord.fixer_model_requested = $FixerModel }
if ($FixerEffortExplicit) { $evalRecord.fixer_effort_requested = $FixerEffort }
if ($null -ne $fixerAccessBinding) {
    $evalRecord.access_config_path_requested = $fixerAccessBinding.Path
    $evalRecord.access_config_sha256_requested = $fixerAccessBinding.Sha256
    $evalRecord.access_config_bytes_requested = $fixerAccessBinding.Bytes
}
if ($null -ne $modelPolicyBinding) {
    $evalRecord.model_policy_path_requested = $modelPolicyBinding.Path
    $evalRecord.model_policy_sha256_requested = $modelPolicyBinding.Sha256
    $evalRecord.model_policy_bytes_requested = $modelPolicyBinding.Bytes
}
if ($writerContractRequested -ne '') { $evalRecord.writer_contract_requested = $writerContractRequested }
if ($IsolatedWriters) {
    $evalRecord.isolated_writers_requested = $true
    $evalRecord.max_parallel_requested = $MaxParallel
    $evalRecord.isolation_policy_version_requested = 1
    $evalRecord.isolation_policy_path_requested = $isolationPolicyBinding.Path
    $evalRecord.isolation_policy_sha256_requested = $isolationPolicyBinding.Sha256
    $evalRecord.isolation_policy_bytes_requested = $isolationPolicyBinding.Bytes
}
$evalRecord | ConvertTo-Json -Depth 10 | Set-Content -Encoding utf8 (Join-Path $evalRoot 'eval.json')
Write-Output "Evaluate complete ($EvalMode): $(@($results | Where-Object { $_.terminal_state -eq 'PASS' }).Count)/$($results.Count) PASS. See $evalRoot\eval.json"
