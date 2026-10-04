[CmdletBinding()]
param(
    [ValidateSet('Run', 'Stop', 'Verify', 'Snapshot')]
    [string] $Mode = 'Run',
    [string] $PayloadPath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
[System.Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false

function Read-PrivateJson([string] $Path) {
    if ([string]::IsNullOrEmpty($Path) -or -not [System.IO.Path]::IsPathRooted($Path)) {
        throw 'The private command payload requires an absolute path.'
    }
    return [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8) | ConvertFrom-Json
}

function Resolve-DirectExecutable([string] $Command, [string] $Directory, $Environment, [bool] $SearchCurrentDirectory) {
    if ([string]::IsNullOrEmpty($Command) -or $Command.IndexOf([char]0) -ge 0) {
        throw 'The private command requires a nonempty executable.'
    }
    $HasDirectory = $Command.IndexOfAny([char[]]@([char]47, [char]92, [char]58)) -ge 0
    $HasExtension = -not [string]::IsNullOrEmpty([System.IO.Path]::GetExtension($Command))
    $Names = @()
    if ($HasExtension) { $Names += $Command }
    $Names += $Command.TrimEnd('.') + '.com'
    $Names += $Command.TrimEnd('.') + '.exe'
    $Directories = @()
    if ($HasDirectory -or $SearchCurrentDirectory) { $Directories += $Directory }
    if (-not $HasDirectory) {
        if ($Environment.ContainsKey('PATH')) {
            foreach ($Entry in ([string]$Environment['PATH']).Split(';')) {
                if ($Entry.Length -ne 0) { $Directories += $Entry.Trim([char[]]@([char]34, [char]39)) }
            }
        }
    }
    foreach ($Base in $Directories) {
        foreach ($Name in $Names) {
            $Candidate = [System.IO.Path]::GetFullPath([System.IO.Path]::Combine($Directory, $Base, $Name))
            if ([System.IO.File]::Exists($Candidate)) { return $Candidate }
        }
        if ($HasDirectory) { break }
    }
    throw 'The private command executable does not exist.'
}

function Create-EnvironmentBlock($Environment) {
    $Builder = New-Object System.Text.StringBuilder
    $Keys = [string[]]@($Environment.Keys)
    [System.Array]::Sort($Keys, [System.StringComparer]::OrdinalIgnoreCase)
    foreach ($Key in $Keys) {
        $Value = $Environment[$Key]
        if ($Key.Length -eq 0 -or $Key.IndexOf([char]0) -ge 0 -or $Value -isnot [string] -or $Value.IndexOf([char]0) -ge 0) {
            throw 'The private command environment contains an invalid entry.'
        }
        [void]$Builder.Append($Key).Append('=').Append($Value).Append([char]0)
    }
    if ($Keys.Count -eq 0) { [void]$Builder.Append([char]0) }
    [void]$Builder.Append([char]0)
    return $Builder.ToString()
}

try {
    if ($Mode -ne 'Snapshot') {
        $Payload = Read-PrivateJson $PayloadPath
        if ($Payload.version -ne 1) { throw 'The private command payload version is invalid.' }
        $CommandEnvironment = New-Object 'System.Collections.Generic.Dictionary[string,string]' ([System.StringComparer]::OrdinalIgnoreCase)
        foreach ($Pair in [System.Environment]::GetEnvironmentVariables().GetEnumerator()) {
            $CommandEnvironment[[string]$Pair.Key] = [string]$Pair.Value
        }
        $CommandEnvironment.Remove('PSMODULEPATH') | Out-Null
        foreach ($Pair in $Payload.environment.PSObject.Properties) {
            $CommandEnvironment[$Pair.Name] = [string]$Pair.Value
        }
        $PrivateDirectory = [System.IO.Path]::GetDirectoryName($PayloadPath)
        $env:TEMP = $PrivateDirectory
        $env:TMP = $PrivateDirectory
    }
    $Source = [System.IO.Path]::Combine($PSScriptRoot, 'windows-command-job.cs')
    Add-Type -Path $Source -ReferencedAssemblies 'System.dll', 'System.Core.dll', 'System.Runtime.Serialization.dll'
    if ($Mode -eq 'Stop') {
        if ([LeapMuxCommandJob]::RequestStop([string]$Payload.stopEventName)) { exit 0 }
        exit 2
    }
    if ($Mode -eq 'Verify') {
        $State = Read-PrivateJson ([string]$Payload.statePath)
        $ProcessIds = @($State.members | ForEach-Object { [uint32]$_.pid })
        $CreationTimes = @($State.members | ForEach-Object { [string]$_.creationTime })
        [LeapMuxCommandJob]::Verify($ProcessIds, $CreationTimes, [uint32]$Payload.shutdownDelayMs)
        exit 0
    }
    if ($Mode -eq 'Snapshot') {
        $Records = Get-CimInstance Win32_Process | ForEach-Object {
            [pscustomobject]@{
                ProcessId = $_.ProcessId
                ParentProcessId = $_.ParentProcessId
                CommandLine = $_.CommandLine
                ExecutablePath = $_.ExecutablePath
                CreationTime = [LeapMuxCommandJob]::SnapshotCreationTime($_.ProcessId)
            }
        }
        ConvertTo-Json -InputObject @($Records) -Compress
        exit 0
    }
    if (-not [System.IO.Path]::IsPathRooted([string]$Payload.cwd)) { throw 'The private command working directory is invalid.' }
    $Executable = Resolve-DirectExecutable ([string]$Payload.command) ([string]$Payload.cwd) $CommandEnvironment ([bool]$Payload.searchCurrentDirectory)
    $EnvironmentBlock = Create-EnvironmentBlock $CommandEnvironment
    $Arguments = [string[]]@([string]$Payload.argv0) + [string[]]@($Payload.args)
    $Code = [LeapMuxCommandJob]::Run($Executable, $Arguments, [bool]$Payload.verbatimArguments, [string]$Payload.cwd, $EnvironmentBlock, [string]$Payload.statePath, [string]$Payload.stopEventName, [uint32]$Payload.shutdownDelayMs)
    exit $Code
}
catch {
    [System.Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}
