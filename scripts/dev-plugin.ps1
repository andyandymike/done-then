[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [ValidateSet('Plan', 'Status', 'Install', 'Reinstall', 'Uninstall')]
    [string]$Action = 'Plan',

    [switch]$Apply,

    [string]$RepositoryRoot = (Split-Path -Parent $PSScriptRoot),

    [string]$DoneThenPath = '',

    [string]$CodexCommand = 'codex',

    [switch]$KeepMarketplace
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$pluginName = 'done-then'
$marketplaceName = 'done-then-dev'
$pluginID = "$pluginName@$marketplaceName"

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

    try {
        return [string]::Equals(
            (Get-NormalizedPath -Path $Left),
            (Get-NormalizedPath -Path $Right),
            [System.StringComparison]::OrdinalIgnoreCase
        )
    }
    catch {
        return $false
    }
}

function Resolve-DoneThenRepository {
    param(
        [Parameter(Mandatory)]
        [string]$Path
    )

    $resolved = (Resolve-Path -LiteralPath $Path).Path
    $manifestPath = Join-Path $resolved '.codex-plugin\plugin.json'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        throw "Repository root does not contain .codex-plugin/plugin.json: $resolved"
    }

    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.name -ne $pluginName) {
        throw "Expected plugin name '$pluginName', found '$($manifest.name)'."
    }
    if ([string]::IsNullOrWhiteSpace([string]$manifest.version)) {
        throw 'Plugin manifest version is missing.'
    }

    return [pscustomobject]@{
        Root     = Get-NormalizedPath -Path $resolved
        Manifest = $manifest
    }
}

function Resolve-CommandPath {
    param(
        [Parameter(Mandatory)]
        [AllowEmptyString()]
        [string]$Name,

        [Parameter(Mandatory)]
        [string]$Repository,

        [switch]$Required
    )

    if (-not [string]::IsNullOrWhiteSpace($Name)) {
        $candidate = $Name
        if (-not [System.IO.Path]::IsPathRooted($candidate)) {
            $repositoryCandidate = Join-Path $Repository $candidate
            if (Test-Path -LiteralPath $repositoryCandidate -PathType Leaf) {
                return (Resolve-Path -LiteralPath $repositoryCandidate).Path
            }
        }
        elseif (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }

        $command = Get-Command -Name $candidate -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $command) {
            if (-not [string]::IsNullOrWhiteSpace([string]$command.Path)) {
                return $command.Path
            }
            if (-not [string]::IsNullOrWhiteSpace([string]$command.Source)) {
                return $command.Source
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
        return $null
    }
    try {
        return $text | ConvertFrom-Json
    }
    catch {
        throw "Codex command did not return valid JSON: $Executable $($Arguments -join ' ')"
    }
}

function Get-CodexInventory {
    param(
        [Parameter(Mandatory)]
        [string]$Executable
    )

    $marketplaces = Invoke-CodexJson -Executable $Executable -Arguments @('plugin', 'marketplace', 'list', '--json')
    $plugins = Invoke-CodexJson -Executable $Executable -Arguments @('plugin', 'list', '--available', '--json')
    $marketplace = @($marketplaces.marketplaces | Where-Object { $_.name -eq $marketplaceName }) | Select-Object -First 1
    $installed = @($plugins.installed | Where-Object { $_.pluginId -eq $pluginID }) | Select-Object -First 1
    $available = @($plugins.available | Where-Object { $_.pluginId -eq $pluginID }) | Select-Object -First 1

    return [pscustomobject]@{
        Marketplace = $marketplace
        Installed   = $installed
        Available   = $available
    }
}

function Assert-ScriptOwnedStage {
    param(
        [Parameter(Mandatory)]
        [string]$Candidate,

        [Parameter(Mandatory)]
        [string]$Expected
    )

    if (-not (Test-SamePath -Left $Candidate -Right $Expected)) {
        throw "Refusing to modify unexpected staging path: $Candidate"
    }
}

function Assert-InvokableRuntime {
    param(
        [Parameter(Mandatory)]
        [string]$Path
    )

    $extension = [System.IO.Path]::GetExtension($Path).ToLowerInvariant()
    if ($extension -notin @('.exe', '.com', '.cmd', '.bat', '.ps1')) {
        throw "DoneThen runtime must be an invokable Windows file (.exe, .com, .cmd, .bat, or .ps1): $Path"
    }
}

function New-CachebusterVersion {
    param(
        [Parameter(Mandatory)]
        [string]$Version
    )

    $baseVersion = ($Version -split '\+', 2)[0]
    $stamp = [DateTime]::UtcNow.ToString('yyyyMMddHHmmssfff')
    return "$baseVersion+codex.local.$stamp"
}

function New-StagedPlugin {
    param(
        [Parameter(Mandatory)]
        [string]$Repository,

        [Parameter(Mandatory)]
        [string]$Stage,

        [Parameter(Mandatory)]
        [string]$RuntimePath
    )

    Assert-ScriptOwnedStage -Candidate $Stage -Expected (Join-Path $Repository '.tmp\done-then-dev-marketplace')
    if (Test-Path -LiteralPath $Stage) {
        Remove-Item -LiteralPath $Stage -Recurse -Force
    }

    $stagePlugin = Join-Path $Stage 'plugins\done-then'
    $marketplaceDirectory = Join-Path $Stage '.agents\plugins'
    $runtimeDirectory = Join-Path $stagePlugin 'runtime'
    foreach ($directory in @($stagePlugin, $marketplaceDirectory, $runtimeDirectory)) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }

    foreach ($item in @('.codex-plugin', 'hooks', 'skills', '.mcp.json', 'README.md', 'LICENSE', 'CHANGELOG.md')) {
        $source = Join-Path $Repository $item
        if (-not (Test-Path -LiteralPath $source)) {
            throw "Required plugin package item is missing: $source"
        }
        Copy-Item -LiteralPath $source -Destination (Join-Path $stagePlugin $item) -Recurse -Force
    }

    $manifestPath = Join-Path $stagePlugin '.codex-plugin\plugin.json'
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    $manifest.version = New-CachebusterVersion -Version ([string]$manifest.version)
    Write-Utf8File -Path $manifestPath -Value ((ConvertTo-StableJson -Value $manifest) + [Environment]::NewLine)

    $escapedRuntime = $RuntimePath.Replace("'", "''")
    $launcherToken = ([string]$manifest.version) -replace '[^A-Za-z0-9_-]', '-'
    $launcherPath = Join-Path $runtimeDirectory "invoke-donethen-$launcherToken.ps1"
    $launcher = @"
[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = `$true)]
    [string[]]`$DoneThenArguments
)

& '$escapedRuntime' @DoneThenArguments
`$doneThenSucceeded = `$?
`$doneThenExitCode = `$LASTEXITCODE
if (`$null -ne `$doneThenExitCode) {
    exit `$doneThenExitCode
}
if (-not `$doneThenSucceeded) {
    exit 1
}
exit 0
"@
    Write-Utf8File -Path $launcherPath -Value ($launcher + [Environment]::NewLine)

    $mcpPath = Join-Path $stagePlugin '.mcp.json'
    $mcp = Get-Content -LiteralPath $mcpPath -Raw | ConvertFrom-Json
    $mcp.mcpServers.done_then.command = 'powershell.exe'
    $mcp.mcpServers.done_then.args = @(
        '-NoLogo',
        '-NoProfile',
        '-NonInteractive',
        '-ExecutionPolicy',
        'Bypass',
        '-File',
        $launcherPath,
        'mcp'
    )
    Write-Utf8File -Path $mcpPath -Value ((ConvertTo-StableJson -Value $mcp) + [Environment]::NewLine)

    $hooksPath = Join-Path $stagePlugin 'hooks\hooks.json'
    $hooks = Get-Content -LiteralPath $hooksPath -Raw | ConvertFrom-Json
    $windowsCommand = 'powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "{0}" hook' -f $launcherPath
    foreach ($eventProperty in $hooks.hooks.PSObject.Properties) {
        foreach ($group in @($eventProperty.Value)) {
            foreach ($handler in @($group.hooks)) {
                $handler.commandWindows = $windowsCommand
            }
        }
    }
    Write-Utf8File -Path $hooksPath -Value ((ConvertTo-StableJson -Value $hooks) + [Environment]::NewLine)

    $marketplace = [ordered]@{
        name      = $marketplaceName
        interface = [ordered]@{
            displayName = 'Done Then Dev'
        }
        plugins   = @(
            [ordered]@{
                name     = $pluginName
                source   = [ordered]@{
                    source = 'local'
                    path   = './plugins/done-then'
                }
                policy   = [ordered]@{
                    installation   = 'AVAILABLE'
                    authentication = 'ON_INSTALL'
                }
                category = 'Productivity'
            }
        )
    }
    $marketplacePath = Join-Path $marketplaceDirectory 'marketplace.json'
    Write-Utf8File -Path $marketplacePath -Value ((ConvertTo-StableJson -Value $marketplace) + [Environment]::NewLine)

    return [pscustomobject]@{
        PluginRoot      = $stagePlugin
        MarketplacePath = $marketplacePath
        LauncherPath    = $launcherPath
        Version         = $manifest.version
    }
}

$repository = Resolve-DoneThenRepository -Path $RepositoryRoot
$repositoryRootPath = $repository.Root
$stageRoot = Get-NormalizedPath -Path (Join-Path $repositoryRootPath '.tmp\done-then-dev-marketplace')
$receiptPath = Get-NormalizedPath -Path (Join-Path $repositoryRootPath '.tmp\done-then-dev-install.json')
$runtimePreview = Resolve-CommandPath -Name $DoneThenPath -Repository $repositoryRootPath
$codexPreview = Resolve-CommandPath -Name $CodexCommand -Repository $repositoryRootPath

$plan = [ordered]@{
    schema_version   = '1'
    action           = $Action
    repository_root  = $repositoryRootPath
    stage_root       = $stageRoot
    marketplace      = $marketplaceName
    plugin_id        = $pluginID
    runtime_path     = $runtimePreview
    codex_path       = $codexPreview
    requires_apply   = $Action -in @('Install', 'Reinstall', 'Uninstall')
    direct_user_file_edits = $false
    commands         = [ordered]@{
        install_marketplace = "codex plugin marketplace add `"$stageRoot`" --json"
        install_plugin      = "codex plugin add $pluginID --json"
        remove_plugin       = "codex plugin remove $pluginID --json"
        remove_marketplace  = "codex plugin marketplace remove $marketplaceName --json"
    }
    retained_on_uninstall = @('the caller-provided DoneThen runtime', '%LOCALAPPDATA%\DoneThen evidence', 'Codex hook trust history')
}

if ($Action -eq 'Plan') {
    ConvertTo-StableJson -Value $plan
    return
}

$codexPath = Resolve-CommandPath -Name $CodexCommand -Repository $repositoryRootPath -Required

if ($Action -eq 'Status') {
    $inventory = Get-CodexInventory -Executable $codexPath
    $marketplaceRoot = if ($null -ne $inventory.Marketplace) { [string]$inventory.Marketplace.root } else { $null }
    $status = [ordered]@{
        schema_version          = '1'
        plugin_id               = $pluginID
        installed               = $null -ne $inventory.Installed
        available               = $null -ne $inventory.Available
        marketplace_configured  = $null -ne $inventory.Marketplace
        marketplace_root        = $marketplaceRoot
        script_owned_marketplace = ($null -ne $marketplaceRoot) -and (Test-SamePath -Left $marketplaceRoot -Right $stageRoot)
        stage_exists            = Test-Path -LiteralPath $stageRoot -PathType Container
        receipt_exists          = Test-Path -LiteralPath $receiptPath -PathType Leaf
    }
    ConvertTo-StableJson -Value $status
    return
}

if (-not $Apply) {
    throw "Action '$Action' changes local plugin state. Review -Action Plan, then rerun with -Apply."
}

if (-not $PSCmdlet.ShouldProcess($pluginID, "$Action local development plugin")) {
    ConvertTo-StableJson -Value $plan
    return
}

if ($Action -in @('Install', 'Reinstall')) {
    $runtimePath = Resolve-CommandPath -Name $DoneThenPath -Repository $repositoryRootPath -Required
    Assert-InvokableRuntime -Path $runtimePath
    $inventory = Get-CodexInventory -Executable $codexPath
    if ($Action -eq 'Install' -and $null -ne $inventory.Installed) {
        throw "$pluginID is already installed; use -Action Reinstall."
    }
    if ($null -ne $inventory.Marketplace -and -not (Test-SamePath -Left ([string]$inventory.Marketplace.root) -Right $stageRoot)) {
        throw "Marketplace '$marketplaceName' already points to a different root: $($inventory.Marketplace.root)"
    }

    $staged = New-StagedPlugin -Repository $repositoryRootPath -Stage $stageRoot -RuntimePath $runtimePath
    $marketplaceAdded = $false
    $pluginAdded = $false
    $pluginWasPreviouslyInstalled = $null -ne $inventory.Installed
    try {
        if ($null -eq $inventory.Marketplace) {
            $null = Invoke-CodexJson -Executable $codexPath -Arguments @('plugin', 'marketplace', 'add', $stageRoot, '--json')
            $marketplaceAdded = $true
        }

        $availableInventory = Get-CodexInventory -Executable $codexPath
        if ($null -eq $availableInventory.Available -and $null -eq $availableInventory.Installed) {
            throw "$pluginID was not discoverable after adding its development marketplace."
        }

        $null = Invoke-CodexJson -Executable $codexPath -Arguments @('plugin', 'add', $pluginID, '--json')
        $pluginAdded = $true
        $installedInventory = Get-CodexInventory -Executable $codexPath
        if ($null -eq $installedInventory.Installed) {
            throw "$pluginID did not appear in the installed plugin list."
        }

        $runtimeHash = (Get-FileHash -LiteralPath $runtimePath -Algorithm SHA256).Hash.ToLowerInvariant()
        $receipt = [ordered]@{
            schema_version              = '1'
            plugin_id                   = $pluginID
            marketplace_root            = $stageRoot
            marketplace_added_by_script = $marketplaceAdded
            plugin_version              = $staged.Version
            runtime_path                = $runtimePath
            runtime_sha256              = $runtimeHash
            installed_at_utc            = [DateTime]::UtcNow.ToString('o')
        }
        Write-Utf8File -Path $receiptPath -Value ((ConvertTo-StableJson -Value $receipt) + [Environment]::NewLine)

        ConvertTo-StableJson -Value ([ordered]@{
            ok               = $true
            action           = $Action
            plugin_id        = $pluginID
            plugin_version   = $staged.Version
            marketplace_root = $stageRoot
            runtime_path     = $runtimePath
            next_step        = 'Start a new Codex task, inspect /hooks, and manually trust the staged DoneThen hook definition.'
        })
        return
    }
    catch {
        if ($pluginAdded -and -not $pluginWasPreviouslyInstalled) {
            try {
                $null = Invoke-CodexJson -Executable $codexPath -Arguments @('plugin', 'remove', $pluginID, '--json')
            }
            catch {
                Write-Warning "Install rollback could not remove plugin '$pluginID': $($_.Exception.Message)"
            }
        }
        if ($marketplaceAdded) {
            try {
                $null = Invoke-CodexJson -Executable $codexPath -Arguments @('plugin', 'marketplace', 'remove', $marketplaceName, '--json')
            }
            catch {
                Write-Warning "Install rollback could not remove marketplace '$marketplaceName': $($_.Exception.Message)"
            }
        }
        throw
    }
}

if ($Action -eq 'Uninstall') {
    Assert-ScriptOwnedStage -Candidate $stageRoot -Expected (Join-Path $repositoryRootPath '.tmp\done-then-dev-marketplace')
    $inventory = Get-CodexInventory -Executable $codexPath
    if ($null -ne $inventory.Installed) {
        $null = Invoke-CodexJson -Executable $codexPath -Arguments @('plugin', 'remove', $pluginID, '--json')
    }

    $marketplaceRemoved = $false
    if ($null -ne $inventory.Marketplace) {
        $marketplaceRoot = [string]$inventory.Marketplace.root
        if (-not (Test-SamePath -Left $marketplaceRoot -Right $stageRoot)) {
            Write-Warning "Marketplace '$marketplaceName' points outside the script-owned staging root and was left unchanged: $marketplaceRoot"
        }
        elseif (-not $KeepMarketplace) {
            $null = Invoke-CodexJson -Executable $codexPath -Arguments @('plugin', 'marketplace', 'remove', $marketplaceName, '--json')
            $marketplaceRemoved = $true
        }
    }

    if (-not $KeepMarketplace -and ($null -eq $inventory.Marketplace -or $marketplaceRemoved)) {
        if (Test-Path -LiteralPath $stageRoot) {
            Remove-Item -LiteralPath $stageRoot -Recurse -Force
        }
        if (Test-Path -LiteralPath $receiptPath -PathType Leaf) {
            Remove-Item -LiteralPath $receiptPath -Force
        }
    }

    ConvertTo-StableJson -Value ([ordered]@{
        ok                    = $true
        action                = 'Uninstall'
        plugin_id             = $pluginID
        plugin_removed        = $null -ne $inventory.Installed
        marketplace_removed   = $marketplaceRemoved
        stage_retained        = Test-Path -LiteralPath $stageRoot -PathType Container
        runtime_removed       = $false
        evidence_removed      = $false
        trust_history_removed = $false
    })
    return
}

throw "Unsupported action: $Action"
