[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Low')]
param(
    [ValidateSet('Plan', 'Snapshot', 'Compare', 'Verify')]
    [string]$Action = 'Plan',

    [switch]$Apply,

    [string]$RepositoryRoot = (Split-Path -Parent $PSScriptRoot),

    [string]$BaselinePath = '',

    [string]$JobId = '',

    [string]$DataRoot = '',

    [string]$CodexCommand = 'codex'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function ConvertTo-StableJson {
    param(
        [Parameter(Mandatory)]
        [object]$Value
    )

    return $Value | ConvertTo-Json -Depth 20
}

function Write-Utf8File {
    param(
        [Parameter(Mandatory)]
        [string]$Path,

        [Parameter(Mandatory)]
        [string]$Value
    )

    $encoding = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText($Path, $Value, $encoding)
}

function Get-NormalizedPath {
    param(
        [Parameter(Mandatory)]
        [string]$Path
    )

    return [System.IO.Path]::GetFullPath($Path).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
}

function Test-SamePath {
    param(
        [Parameter(Mandatory)]
        [string]$Left,

        [Parameter(Mandatory)]
        [string]$Right
    )

    return [string]::Equals(
        (Get-NormalizedPath -Path $Left),
        (Get-NormalizedPath -Path $Right),
        [System.StringComparison]::OrdinalIgnoreCase
    )
}

function Assert-PathWithin {
    param(
        [Parameter(Mandatory)]
        [string]$Candidate,

        [Parameter(Mandatory)]
        [string]$Parent
    )

    $candidatePath = Get-NormalizedPath -Path $Candidate
    $parentPath = Get-NormalizedPath -Path $Parent
    $prefix = $parentPath + [System.IO.Path]::DirectorySeparatorChar
    if (-not $candidatePath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Path must stay inside $parentPath`: $candidatePath"
    }
}

function Resolve-CommandPath {
    param(
        [Parameter(Mandatory)]
        [AllowEmptyString()]
        [string]$Name,

        [switch]$Required
    )

    if (-not [string]::IsNullOrWhiteSpace($Name)) {
        if ([System.IO.Path]::IsPathRooted($Name) -and (Test-Path -LiteralPath $Name -PathType Leaf)) {
            return (Resolve-Path -LiteralPath $Name).Path
        }
        $command = Get-Command -Name $Name -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $command) {
            $pathProperty = $command.PSObject.Properties['Path']
            if ($null -ne $pathProperty -and -not [string]::IsNullOrWhiteSpace([string]$pathProperty.Value)) {
                return [string]$pathProperty.Value
            }
            $sourceProperty = $command.PSObject.Properties['Source']
            if ($null -ne $sourceProperty -and -not [string]::IsNullOrWhiteSpace([string]$sourceProperty.Value)) {
                return [string]$sourceProperty.Value
            }
        }
    }

    if ($Required) {
        throw "Command or file was not found: $Name"
    }
    return $null
}

function Invoke-CodexJson {
    param(
        [Parameter(Mandatory)]
        [string]$Executable,

        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    $raw = & $Executable @Arguments 2>&1
    $exitCode = $LASTEXITCODE
    $text = ($raw | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine
    if ($exitCode -ne 0) {
        throw "Codex command failed (exit $exitCode): $Executable $($Arguments -join ' ')`n$text"
    }
    if ([string]::IsNullOrWhiteSpace($text)) {
        throw 'Codex plugin inventory returned no JSON.'
    }
    try {
        return $text | ConvertFrom-Json
    }
    catch {
        throw 'Codex plugin inventory did not return valid JSON.'
    }
}

function Get-BaseTargets {
    param(
        [Parameter(Mandatory)]
        [string]$Repository
    )

    $userProfile = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    return @(
        [ordered]@{ name = 'user_hooks_json'; path = Join-Path $userProfile '.codex\hooks.json' }
        [ordered]@{ name = 'user_config_toml'; path = Join-Path $userProfile '.codex\config.toml' }
        [ordered]@{ name = 'repository_hooks_json'; path = Join-Path $Repository '.codex\hooks.json' }
        [ordered]@{ name = 'repository_config_toml'; path = Join-Path $Repository '.codex\config.toml' }
    )
}

function Get-SafeTreeFiles {
    param(
        [Parameter(Mandatory)]
        [string]$Directory
    )

    $pending = [System.Collections.Generic.Stack[string]]::new()
    $pending.Push((Get-NormalizedPath -Path $Directory))
    while ($pending.Count -ne 0) {
        $current = $pending.Pop()
        foreach ($item in Get-ChildItem -LiteralPath $current -Force) {
            if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Plugin Hook tree contains a reparse point that cannot be inventoried safely: $($item.FullName)"
            }
            if ($item.PSIsContainer) {
                $pending.Push($item.FullName)
            }
            else {
                $item.FullName
            }
        }
    }
}

function Get-OtherPluginTargets {
    param(
        [Parameter(Mandatory)]
        [string]$CodexExecutable
    )

    $inventory = Invoke-CodexJson -Executable $CodexExecutable -Arguments @('plugin', 'list', '--json')
    $targets = [System.Collections.Generic.List[object]]::new()
    foreach ($plugin in @($inventory.installed)) {
        $pluginID = [string]$plugin.pluginId
        if ([string]::IsNullOrWhiteSpace($pluginID)) {
            throw 'Codex returned an installed plugin without pluginId.'
        }
        if ($pluginID -like 'done-then@*' -or [string]$plugin.name -eq 'done-then') {
            continue
        }

        $sourceProperty = $plugin.PSObject.Properties['source']
        $sourcePath = if ($null -ne $sourceProperty -and $null -ne $sourceProperty.Value) { [string]$sourceProperty.Value.path } else { '' }
        if ([string]::IsNullOrWhiteSpace($sourcePath)) {
            throw "Codex did not expose a local source path for installed plugin $pluginID."
        }
        $pluginRoot = Get-NormalizedPath -Path $sourcePath
        $manifestPath = Join-Path $pluginRoot '.codex-plugin\plugin.json'
        if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
            throw "Installed plugin manifest is unavailable for $pluginID`: $manifestPath"
        }
        $targets.Add([ordered]@{ name = "plugin:$pluginID`:manifest"; path = $manifestPath }) | Out-Null

        $pluginManifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
        $hookPaths = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
        $hooksProperty = $pluginManifest.PSObject.Properties['hooks']
        if ($null -ne $hooksProperty -and $hooksProperty.Value -is [string]) {
            $hookPaths.Add([string]$hooksProperty.Value) | Out-Null
        }
        elseif ($null -ne $hooksProperty -and $hooksProperty.Value -is [System.Collections.IEnumerable]) {
            foreach ($hookValue in $hooksProperty.Value) {
                if ($hookValue -is [string]) {
                    $hookPaths.Add([string]$hookValue) | Out-Null
                }
            }
        }

        $hooksDirectory = Join-Path $pluginRoot 'hooks'
        if (Test-Path -LiteralPath $hooksDirectory -PathType Container) {
            foreach ($hookTreeFile in Get-SafeTreeFiles -Directory $hooksDirectory) {
                $hookPaths.Add([System.IO.Path]::GetRelativePath($pluginRoot, $hookTreeFile)) | Out-Null
            }
        }

        foreach ($hookPathValue in $hookPaths) {
            if ([System.IO.Path]::IsPathRooted($hookPathValue)) {
                throw "Installed plugin $pluginID declares an absolute Hook path."
            }
            $relativeHookPath = $hookPathValue -replace '^[.][\\/]', ''
            $hookPath = Get-NormalizedPath -Path (Join-Path $pluginRoot $relativeHookPath)
            $pluginPrefix = $pluginRoot + [System.IO.Path]::DirectorySeparatorChar
            if (-not $hookPath.StartsWith($pluginPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
                throw "Installed plugin $pluginID declares a Hook path outside its root."
            }
            if (-not (Test-Path -LiteralPath $hookPath -PathType Leaf)) {
                throw "Installed plugin Hook file is unavailable for $pluginID`: $hookPath"
            }
            $portableRelativePath = $relativeHookPath.Replace('\', '/')
            $targets.Add([ordered]@{ name = "plugin:$pluginID`:hook:$portableRelativePath"; path = $hookPath }) | Out-Null
        }
    }
    return @($targets)
}

function Get-AllTargets {
    param(
        [Parameter(Mandatory)]
        [string]$Repository,

        [Parameter(Mandatory)]
        [string]$CodexExecutable
    )

    return @(
        Get-BaseTargets -Repository $Repository
        Get-OtherPluginTargets -CodexExecutable $CodexExecutable
    )
}

function Get-FileFingerprint {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [string]$Path
    )

    $absolute = Get-NormalizedPath -Path $Path
    if (-not (Test-Path -LiteralPath $absolute)) {
        return [ordered]@{
            name   = $Name
            path   = $absolute
            exists = $false
            length = 0
            sha256 = $null
        }
    }
    if (-not (Test-Path -LiteralPath $absolute -PathType Leaf)) {
        throw "Expected a file or an absent path: $absolute"
    }

    $file = Get-Item -LiteralPath $absolute
    return [ordered]@{
        name   = $Name
        path   = $absolute
        exists = $true
        length = $file.Length
        sha256 = (Get-FileHash -LiteralPath $absolute -Algorithm SHA256).Hash.ToLowerInvariant()
    }
}

function Get-ConfigurationSnapshot {
    param(
        [Parameter(Mandatory)]
        [string]$Repository,

        [Parameter(Mandatory)]
        [string]$CodexExecutable
    )

    $fingerprints = foreach ($target in Get-AllTargets -Repository $Repository -CodexExecutable $CodexExecutable) {
        Get-FileFingerprint -Name $target.name -Path $target.path
    }
    return @($fingerprints)
}

function Add-ConfigurationFailures {
    param(
        [Parameter(Mandatory)]
        [object[]]$Baseline,

        [Parameter(Mandatory)]
        [object[]]$Current,

        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [System.Collections.Generic.List[string]]$Failures
    )

    $baselineByName = @{}
    foreach ($item in $Baseline) {
        $baselineByName[[string]$item.name] = $item
    }
    foreach ($item in $Current) {
        $name = [string]$item.name
        if (-not $baselineByName.ContainsKey($name)) {
            $Failures.Add("baseline is missing configuration target $name") | Out-Null
            continue
        }
        $before = $baselineByName[$name]
        if (-not (Test-SamePath -Left ([string]$before.path) -Right ([string]$item.path))) {
            $Failures.Add("configuration target path changed for $name") | Out-Null
            continue
        }
        if ([bool]$before.exists -ne [bool]$item.exists) {
            $Failures.Add("configuration target existence changed for $name") | Out-Null
            continue
        }
        if ([bool]$item.exists -and [string]$before.sha256 -ne [string]$item.sha256) {
            $Failures.Add("configuration target content changed for $name") | Out-Null
        }
    }
    if ($baselineByName.Count -ne $Current.Count) {
        $Failures.Add('configuration baseline contains an unexpected number of targets') | Out-Null
    }
}

$repositoryRootPath = Get-NormalizedPath -Path (Resolve-Path -LiteralPath $RepositoryRoot).Path
$manifestPath = Join-Path $repositoryRootPath '.codex-plugin\plugin.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "Repository root does not contain .codex-plugin/plugin.json: $repositoryRootPath"
}
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ($manifest.name -ne 'done-then') {
    throw "Expected the done-then plugin repository, found '$($manifest.name)'."
}

$smokeRoot = Get-NormalizedPath -Path (Join-Path $repositoryRootPath '.tmp\live-smoke')
if ([string]::IsNullOrWhiteSpace($BaselinePath)) {
    $baselineFile = Join-Path $smokeRoot 'baseline.json'
}
elseif ([System.IO.Path]::IsPathRooted($BaselinePath)) {
    $baselineFile = Get-NormalizedPath -Path $BaselinePath
}
else {
    $baselineFile = Get-NormalizedPath -Path (Join-Path $repositoryRootPath $BaselinePath)
}
Assert-PathWithin -Candidate $baselineFile -Parent $smokeRoot

if ([string]::IsNullOrWhiteSpace($DataRoot)) {
    $localApplicationData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    $dataRootPath = Get-NormalizedPath -Path (Join-Path $localApplicationData 'DoneThen')
}
else {
    $dataRootPath = Get-NormalizedPath -Path $DataRoot
}
$codexPreview = Resolve-CommandPath -Name $CodexCommand

$plan = [ordered]@{
    schema_version       = '1'
    action               = $Action
    repository_root      = $repositoryRootPath
    baseline_path        = $baselineFile
    data_root            = $dataRootPath
    codex_path           = $codexPreview
    snapshot_targets     = @(
        @(Get-BaseTargets -Repository $repositoryRootPath)
        'installed non-DoneThen plugin manifests and Hook files from codex plugin list --json'
    )
    snapshot_writes_only = $baselineFile
    invokes_shutdown     = $false
    installs_plugin      = $false
    changes_hook_trust   = $false
    verify_requires      = @('unchanged configuration hashes', 'unchanged installed runtime hash', 'after_stop dry_run job', 'DRY_RUN_COMPLETE state', 'arm/bind/Stop event subsequence', 'redacted event schema')
}

if ($Action -eq 'Plan') {
    ConvertTo-StableJson -Value $plan
    return
}

if ($Action -eq 'Snapshot') {
    if (-not $Apply) {
        throw 'Snapshot writes an ignored local baseline. Review -Action Plan, then rerun with -Apply.'
    }
    if (-not $PSCmdlet.ShouldProcess($baselineFile, 'Write live-smoke configuration baseline')) {
        ConvertTo-StableJson -Value $plan
        return
    }

    New-Item -ItemType Directory -Path $smokeRoot -Force | Out-Null
    $codexPath = Resolve-CommandPath -Name $CodexCommand -Required
    $baseline = [ordered]@{
        schema_version  = '1'
        captured_at_utc = [DateTime]::UtcNow.ToString('o')
        repository_root = $repositoryRootPath
        targets         = @(Get-ConfigurationSnapshot -Repository $repositoryRootPath -CodexExecutable $codexPath)
    }
    Write-Utf8File -Path $baselineFile -Value ((ConvertTo-StableJson -Value $baseline) + [Environment]::NewLine)
    ConvertTo-StableJson -Value ([ordered]@{
        ok            = $true
        action        = 'Snapshot'
        baseline_path = $baselineFile
        target_count  = $baseline.targets.Count
        next_step     = 'Install/trust the development plugin, run one new dry-run task, then verify its job_id.'
    })
    return
}

if ($Action -notin @('Compare', 'Verify')) {
    throw "Unsupported action: $Action"
}
if (-not (Test-Path -LiteralPath $baselineFile -PathType Leaf)) {
    throw "Live-smoke baseline does not exist: $baselineFile"
}

$baseline = Get-Content -LiteralPath $baselineFile -Raw | ConvertFrom-Json
if ($baseline.schema_version -ne '1') {
    throw "Unsupported live-smoke baseline schema: $($baseline.schema_version)"
}
if (-not (Test-SamePath -Left ([string]$baseline.repository_root) -Right $repositoryRootPath)) {
    throw 'Live-smoke baseline belongs to a different repository root.'
}

$failures = [System.Collections.Generic.List[string]]::new()
$codexPath = Resolve-CommandPath -Name $CodexCommand -Required
$currentConfiguration = @(Get-ConfigurationSnapshot -Repository $repositoryRootPath -CodexExecutable $codexPath)
Add-ConfigurationFailures -Baseline @($baseline.targets) -Current $currentConfiguration -Failures $failures
$configurationFailureCount = $failures.Count

if ($Action -eq 'Compare') {
    $report = [ordered]@{
        schema_version          = '1'
        ok                      = $failures.Count -eq 0
        action                  = 'Compare'
        baseline_path           = $baselineFile
        configuration_unchanged = $configurationFailureCount -eq 0
        target_count            = $currentConfiguration.Count
        failures                = @($failures)
    }
    ConvertTo-StableJson -Value $report
    if ($failures.Count -ne 0) {
        exit 1
    }
    return
}

if ($JobId -notmatch '^dt_[A-Za-z0-9_-]{3,77}$') {
    throw 'Verify requires a valid -JobId beginning with dt_.'
}

$runtimeIdentityVerified = $false
$installReceiptPath = Join-Path $repositoryRootPath '.tmp\done-then-dev-install.json'
if (-not (Test-Path -LiteralPath $installReceiptPath -PathType Leaf)) {
    $failures.Add('development install receipt is missing') | Out-Null
}
else {
    try {
        $installReceipt = Get-Content -LiteralPath $installReceiptPath -Raw | ConvertFrom-Json
        if ([string]$installReceipt.schema_version -ne '1' -or [string]$installReceipt.plugin_id -ne 'done-then@done-then-dev') {
            $failures.Add('development install receipt has an invalid identity') | Out-Null
        }
        elseif (-not (Test-Path -LiteralPath ([string]$installReceipt.runtime_path) -PathType Leaf)) {
            $failures.Add('installed DoneThen runtime is missing') | Out-Null
        }
        else {
            $currentRuntimeHash = (Get-FileHash -LiteralPath ([string]$installReceipt.runtime_path) -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($currentRuntimeHash -ne [string]$installReceipt.runtime_sha256) {
                $failures.Add('installed DoneThen runtime hash changed after install') | Out-Null
            }
            else {
                $runtimeIdentityVerified = $true
            }
        }
    }
    catch {
        $failures.Add('development install receipt is unreadable') | Out-Null
    }
}

$jobPath = Join-Path $dataRootPath "plugin\jobs\$JobId.json"
$eventPath = Join-Path $dataRootPath "plugin\events\$JobId.jsonl"
if (-not (Test-Path -LiteralPath $jobPath -PathType Leaf)) {
    $failures.Add("plugin job record is missing: $jobPath") | Out-Null
}
if (-not (Test-Path -LiteralPath $eventPath -PathType Leaf)) {
    $failures.Add("plugin event log is missing: $eventPath") | Out-Null
}

$job = $null
$events = @()
if (Test-Path -LiteralPath $jobPath -PathType Leaf) {
    try {
        $job = Get-Content -LiteralPath $jobPath -Raw | ConvertFrom-Json
    }
    catch {
        $failures.Add('plugin job record is not valid JSON') | Out-Null
    }
}
if (Test-Path -LiteralPath $eventPath -PathType Leaf) {
    $eventList = [System.Collections.Generic.List[object]]::new()
    foreach ($line in Get-Content -LiteralPath $eventPath) {
        if ([string]::IsNullOrWhiteSpace($line)) {
            continue
        }
        try {
            $eventList.Add(($line | ConvertFrom-Json)) | Out-Null
        }
        catch {
            $failures.Add('plugin event log contains invalid JSON') | Out-Null
        }
    }
    $events = @($eventList)
}

if ($null -ne $job) {
    $jobChecks = [ordered]@{
        schema_version           = [string]$job.schema_version -eq '3'
        identity                 = [string]$job.job_id -eq $JobId
        state                    = [string]$job.state -eq 'DRY_RUN_COMPLETE'
        reason                   = [string]$job.reason_code -eq 'after_stop_observed_no_action'
        dry_run                  = [bool]$job.dry_run
        shutdown_intent          = [string]$job.action -eq 'shutdown'
        trigger_policy           = [string]$job.trigger_policy -eq 'after_stop'
        stop_without_success_ack = -not [bool]$job.stop_without_success_acknowledged
        semantic_verifier_absent = [string]$job.verifier_profile -eq 'none' -and -not [bool]$job.allow_agent_only_success
        hook_compatibility       = [string]$job.hook_compatibility -eq 'session_bound'
        arm_observed             = [bool]$job.arm_observed
        finish_not_observed      = -not [bool]$job.finish_observed
        no_completion_status     = [string]::IsNullOrWhiteSpace([string]$job.completion_status)
        no_completion_evidence   = [string]::IsNullOrWhiteSpace([string]$job.completion_evidence_hash)
        stop_turn_bound          = -not [string]::IsNullOrWhiteSpace([string]$job.stop_turn_id)
    }
    foreach ($check in $jobChecks.GetEnumerator()) {
        if (-not [bool]$check.Value) {
            $failures.Add("plugin job check failed: $($check.Key)") | Out-Null
        }
    }
}
else {
    $jobChecks = [ordered]@{}
}

$allowedEventFields = @(
    'schema_version', 'timestamp', 'job_id', 'name', 'event_key', 'old_state',
    'new_state', 'reason_code', 'generation', 'session_hash', 'turn_hash'
)
$forbiddenEventFields = @('prompt', 'transcript', 'session_id', 'turn_id', 'tool_input', 'tool_response', 'nonce', 'environment')
$powerActionEventCount = 0
foreach ($event in $events) {
    $properties = @($event.PSObject.Properties.Name)
    foreach ($property in $properties) {
        if ($property -notin $allowedEventFields) {
            $failures.Add("plugin event contains unexpected field: $property") | Out-Null
        }
        if ($property -in $forbiddenEventFields) {
            $failures.Add("plugin event contains forbidden field: $property") | Out-Null
        }
    }
    if ([string]$event.schema_version -ne '1' -or [string]$event.job_id -ne $JobId) {
        $failures.Add('plugin event has an invalid schema or job identity') | Out-Null
    }
    foreach ($hashField in @('event_key', 'session_hash', 'turn_hash')) {
        $hashProperty = $event.PSObject.Properties[$hashField]
        if ($null -ne $hashProperty -and -not [string]::IsNullOrWhiteSpace([string]$hashProperty.Value) -and [string]$hashProperty.Value -notmatch '^[0-9a-f]{64}$') {
            $failures.Add("plugin event has an invalid $hashField") | Out-Null
        }
    }
    if ([string]$event.name -match '(?i)(power|shutdown|execute|schedule)') {
        $powerActionEventCount++
    }
}
if ($powerActionEventCount -ne 0) {
    $failures.Add('plugin event log contains a power-action event') | Out-Null
}

$expectedSequence = @('mcp.arm', 'hook.post_tool.arm', 'hook.stop')
$sequencePosition = 0
foreach ($event in $events) {
    if ($sequencePosition -lt $expectedSequence.Count -and [string]$event.name -eq $expectedSequence[$sequencePosition]) {
        $sequencePosition++
    }
}
$eventSequenceObserved = $sequencePosition -eq $expectedSequence.Count
if (-not $eventSequenceObserved) {
    $failures.Add("required event sequence stopped at position $sequencePosition of $($expectedSequence.Count)") | Out-Null
}

$report = [ordered]@{
    schema_version             = '1'
    ok                         = $failures.Count -eq 0
    action                     = 'Verify'
    job_id                     = $JobId
    baseline_path              = $baselineFile
    configuration_unchanged    = $configurationFailureCount -eq 0
    runtime_identity_verified  = $runtimeIdentityVerified
    job_checks                 = $jobChecks
    event_count                = $events.Count
    event_sequence_observed    = $eventSequenceObserved
    power_action_event_count   = $powerActionEventCount
    after_stop_execute_available_expected = $true
    failures                   = @($failures)
}
ConvertTo-StableJson -Value $report
if ($failures.Count -ne 0) {
    exit 1
}
