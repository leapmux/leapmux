# Samples the resources of a Windows CI runner while the job runs.
#
# GitHub uploads no log for a job whose runner it lost, so the monitor
# publishes its samples to a check run of its own. That output stays readable
# after GitHub declares the runner lost, and as long as the machine can still
# reach GitHub, it shows what happened to the runner afterwards.
#
# The monitor reads every value inside its own process. When Windows can no
# longer start a process, it keeps sampling; one canary process for each sample
# records when process creation started to fail.
#
# Modes:
#   Start  creates the check run and starts the monitor as a detached process.
#   Run    samples until the Stop mode asks it to end. Start runs this mode.
#   Stop   ends the monitor, completes the check run, and prints the log.
[CmdletBinding()]
param(
    [ValidateSet('Start', 'Run', 'Stop')]
    [string] $Mode = 'Start',
    [string] $StateDirectory = $env:RUNNER_TEMP,
    [ValidateRange(1, 3600)]
    [int] $SampleSeconds = 10,
    [ValidateRange(1, 3600)]
    [int] $PublishSeconds = 30
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# A check run accepts at most 65535 characters in each output field.
$MaxOutputCharacters = 60000
# Every sample that is a multiple of this count also records the process tables.
$DetailEvery = 6

if ([string]::IsNullOrEmpty($StateDirectory)) {
    throw 'The resource monitor requires -StateDirectory or RUNNER_TEMP.'
}
$Directory = Join-Path $StateDirectory 'leapmux-resource-monitor'
$LogPath = Join-Path $Directory 'monitor.log'
$PidPath = Join-Path $Directory 'monitor.pid'
$CheckRunPath = Join-Path $Directory 'check-run-id'
$StopPath = Join-Path $Directory 'stop'

function Add-MonitorLog([string] $Text) {
    [System.IO.File]::AppendAllText($LogPath, $Text + [Environment]::NewLine, [System.Text.Encoding]::UTF8)
}

function Get-ApiBase {
    $Base = $env:GITHUB_API_URL
    if ([string]::IsNullOrEmpty($Base)) { $Base = 'https://api.github.com' }
    return $Base.TrimEnd('/')
}

# Invoke-GitHub sends one REST request. It returns $null when the job supplied
# no token, so a run without a token (a fork) still keeps the local log.
function Invoke-GitHub([string] $Method, [string] $Path, $Body) {
    if ([string]::IsNullOrEmpty($env:GH_TOKEN) -or [string]::IsNullOrEmpty($env:GITHUB_REPOSITORY)) {
        return $null
    }
    $Headers = @{
        Authorization          = "Bearer $($env:GH_TOKEN)"
        Accept                 = 'application/vnd.github+json'
        'X-GitHub-Api-Version' = '2022-11-28'
    }
    $Uri = "$(Get-ApiBase)/repos/$($env:GITHUB_REPOSITORY)$Path"
    $Json = $Body | ConvertTo-Json -Depth 6 -Compress
    return Invoke-RestMethod -Method $Method -Uri $Uri -Headers $Headers -Body $Json -ContentType 'application/json' -TimeoutSec 20
}

function Read-CheckRunId {
    if (-not (Test-Path -LiteralPath $CheckRunPath)) { return $null }
    $Text = [System.IO.File]::ReadAllText($CheckRunPath).Trim()
    if ($Text.Length -eq 0) { return $null }
    return $Text
}

function Limit-Text([string] $Text) {
    if ($Text.Length -le $MaxOutputCharacters) { return $Text }
    return '...' + $Text.Substring($Text.Length - $MaxOutputCharacters)
}

function Start-Monitor {
    [System.IO.Directory]::CreateDirectory($Directory) | Out-Null
    foreach ($Path in @($StopPath, $CheckRunPath, $PidPath)) {
        if (Test-Path -LiteralPath $Path) { Remove-Item -LiteralPath $Path -Force }
    }
    $Name = $env:MONITOR_CHECK_NAME
    if ([string]::IsNullOrEmpty($Name)) { $Name = 'Windows resource monitor' }
    $Sha = $env:MONITOR_HEAD_SHA
    if ([string]::IsNullOrEmpty($Sha)) { $Sha = $env:GITHUB_SHA }
    $DetailsUrl = "$($env:GITHUB_SERVER_URL)/$($env:GITHUB_REPOSITORY)/actions/runs/$($env:GITHUB_RUN_ID)"
    try {
        $Created = Invoke-GitHub -Method 'Post' -Path '/check-runs' -Body @{
            name        = $Name
            head_sha    = $Sha
            status      = 'in_progress'
            details_url = $DetailsUrl
            output      = @{ title = 'Starting'; summary = 'The monitor publishes its first sample shortly.' }
        }
        if ($null -ne $Created) {
            [System.IO.File]::WriteAllText($CheckRunPath, [string]$Created.id)
            Write-Host "The resource monitor publishes to check run $($Created.id): $($Created.html_url)"
        }
        else {
            Write-Host 'The resource monitor has no token, so it keeps a local log only.'
        }
    }
    catch {
        Write-Warning "The resource monitor could not create its check run, so it keeps a local log only: $_"
    }

    # ShellExecute starts the monitor without inheriting a handle. With the
    # step's output pipes, the step would wait for the monitor to exit.
    $StartInfo = [System.Diagnostics.ProcessStartInfo]::new([Environment]::ProcessPath)
    foreach ($Argument in @(
            '-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass',
            '-File', $PSCommandPath, '-Mode', 'Run', '-StateDirectory', $StateDirectory,
            '-SampleSeconds', [string]$SampleSeconds, '-PublishSeconds', [string]$PublishSeconds)) {
        $StartInfo.ArgumentList.Add($Argument)
    }
    $StartInfo.UseShellExecute = $true
    $StartInfo.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden
    $Process = [System.Diagnostics.Process]::Start($StartInfo)
    [System.IO.File]::WriteAllText($PidPath, [string]$Process.Id)
    Write-Host "Started the resource monitor as process $($Process.Id). Its log is $LogPath."
}

function Stop-Monitor {
    if (-not (Test-Path -LiteralPath $Directory)) {
        Write-Host 'The resource monitor did not start.'
        return
    }
    [System.IO.File]::WriteAllText($StopPath, '')
    # A monitor that ends by itself completes its check run. Otherwise this
    # mode completes it, so that the check run does not stay in progress.
    $EndedByItself = $false
    if (Test-Path -LiteralPath $PidPath) {
        $MonitorId = [int]([System.IO.File]::ReadAllText($PidPath).Trim())
        $Monitor = Get-Process -Id $MonitorId -ErrorAction SilentlyContinue
        if ($null -eq $Monitor) {
            Write-Warning "The resource monitor $MonitorId ended before the job asked it to stop."
        }
        elseif ($Monitor.WaitForExit(60000)) {
            $EndedByItself = $true
        }
        else {
            Write-Warning "The resource monitor $MonitorId did not end within 60 seconds. Stopping it."
            Stop-Process -Id $MonitorId -Force -ErrorAction SilentlyContinue
        }
    }
    $CheckRunId = Read-CheckRunId
    if (-not $EndedByItself -and $null -ne $CheckRunId) {
        $Tail = ''
        if (Test-Path -LiteralPath $LogPath) { $Tail = (Get-Content -LiteralPath $LogPath -Tail 200) -join "`n" }
        $Fence = '```'
        try {
            Publish-Output -CheckRunId $CheckRunId -Title 'The monitor ended early' -Summary 'The monitor ended before the job asked it to stop. The text holds the end of its log.' -Text "$Fence`n$Tail`n$Fence" -Complete $true
        }
        catch {
            Write-Warning "The resource monitor could not complete its check run: $_"
        }
    }
    Write-Host '::group::Resource monitor log'
    if (Test-Path -LiteralPath $LogPath) {
        Get-Content -LiteralPath $LogPath | ForEach-Object { Write-Host $_ }
    }
    else {
        Write-Host 'The resource monitor wrote no log.'
    }
    Write-Host '::endgroup::'
}

$ProbeSource = @'
using System;
using System.Runtime.InteropServices;
using System.Text;

public static class LeapMuxResourceProbe
{
    [StructLayout(LayoutKind.Sequential)]
    private struct PerformanceInformation
    {
        public uint Size;
        public UIntPtr CommitTotal;
        public UIntPtr CommitLimit;
        public UIntPtr CommitPeak;
        public UIntPtr PhysicalTotal;
        public UIntPtr PhysicalAvailable;
        public UIntPtr SystemCache;
        public UIntPtr KernelTotal;
        public UIntPtr KernelPaged;
        public UIntPtr KernelNonpaged;
        public UIntPtr PageSize;
        public uint HandleCount;
        public uint ProcessCount;
        public uint ThreadCount;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ProcessBasicInformation
    {
        public IntPtr ExitStatus;
        public IntPtr PebBaseAddress;
        public IntPtr AffinityMask;
        public IntPtr BasePriority;
        public IntPtr UniqueProcessId;
        public IntPtr InheritedFromUniqueProcessId;
    }

    [DllImport("psapi.dll", SetLastError = true)]
    private static extern bool GetPerformanceInfo(out PerformanceInformation information, uint size);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool GetSystemTimes(out long idle, out long kernel, out long user);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern IntPtr OpenProcess(uint access, bool inherit, int processId);

    [DllImport("kernel32.dll")]
    private static extern bool CloseHandle(IntPtr handle);

    [DllImport("kernel32.dll")]
    private static extern uint GetCurrentThreadId();

    [DllImport("user32.dll")]
    private static extern uint GetGuiResources(IntPtr process, uint flags);

    [DllImport("user32.dll")]
    private static extern IntPtr GetProcessWindowStation();

    [DllImport("user32.dll")]
    private static extern IntPtr GetThreadDesktop(uint threadId);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool GetUserObjectInformationW(IntPtr handle, int index, byte[] information, uint length, out uint needed);

    [DllImport("ntdll.dll")]
    private static extern int NtQueryInformationProcess(IntPtr process, int informationClass, ref ProcessBasicInformation information, int length, out int returned);

    private const uint ProcessQueryLimitedInformation = 0x1000;
    private const uint GuiGdiObjects = 0;
    private const uint GuiUserObjects = 1;
    private const int UoiName = 2;
    private const int UoiHeapSize = 5;

    // Performance returns the system totals in bytes, then the handle,
    // process and thread counts:
    // commit, commit limit, commit peak, physical total, physical available,
    // kernel paged, kernel nonpaged, handles, processes, threads.
    public static long[] Performance()
    {
        PerformanceInformation information;
        if (!GetPerformanceInfo(out information, (uint)Marshal.SizeOf(typeof(PerformanceInformation))))
        {
            throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        }
        long page = (long)information.PageSize.ToUInt64();
        return new long[]
        {
            (long)information.CommitTotal.ToUInt64() * page,
            (long)information.CommitLimit.ToUInt64() * page,
            (long)information.CommitPeak.ToUInt64() * page,
            (long)information.PhysicalTotal.ToUInt64() * page,
            (long)information.PhysicalAvailable.ToUInt64() * page,
            (long)information.KernelPaged.ToUInt64() * page,
            (long)information.KernelNonpaged.ToUInt64() * page,
            information.HandleCount,
            information.ProcessCount,
            information.ThreadCount,
        };
    }

    // SystemTimes returns the idle, kernel and user times of all processors
    // in 100-nanosecond units. The kernel time includes the idle time.
    public static long[] SystemTimes()
    {
        long idle, kernel, user;
        if (!GetSystemTimes(out idle, out kernel, out user))
        {
            throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        }
        return new long[] { idle, kernel, user };
    }

    // ProcessDetails returns the parent process ID, the USER object count and
    // the GDI object count of a process. Each value is -1 when the process
    // cannot be opened. USER objects live in the desktop heap.
    public static long[] ProcessDetails(int processId)
    {
        long[] result = new long[] { -1, -1, -1 };
        IntPtr handle = OpenProcess(ProcessQueryLimitedInformation, false, processId);
        if (handle == IntPtr.Zero)
        {
            return result;
        }
        try
        {
            ProcessBasicInformation basic = new ProcessBasicInformation();
            int returned;
            if (NtQueryInformationProcess(handle, 0, ref basic, Marshal.SizeOf(typeof(ProcessBasicInformation)), out returned) == 0)
            {
                result[0] = basic.InheritedFromUniqueProcessId.ToInt64();
            }
            result[1] = GetGuiResources(handle, GuiUserObjects);
            result[2] = GetGuiResources(handle, GuiGdiObjects);
        }
        finally
        {
            CloseHandle(handle);
        }
        return result;
    }

    private static byte[] UserObjectInformation(IntPtr handle, int index)
    {
        uint needed;
        GetUserObjectInformationW(handle, index, null, 0, out needed);
        if (needed == 0)
        {
            return null;
        }
        byte[] buffer = new byte[needed];
        if (!GetUserObjectInformationW(handle, index, buffer, needed, out needed))
        {
            return null;
        }
        return buffer;
    }

    private static string UserObjectName(IntPtr handle)
    {
        byte[] buffer = UserObjectInformation(handle, UoiName);
        return buffer == null ? "unknown" : Encoding.Unicode.GetString(buffer).TrimEnd('\0');
    }

    // Desktop returns the window station and desktop of this thread, with the
    // size of that desktop's heap in kilobytes.
    public static string Desktop()
    {
        IntPtr desktop = GetThreadDesktop(GetCurrentThreadId());
        byte[] heap = UserObjectInformation(desktop, UoiHeapSize);
        string heapSize = heap == null || heap.Length < 4 ? "unknown" : BitConverter.ToUInt32(heap, 0) + "KB";
        return UserObjectName(GetProcessWindowStation()) + "\\" + UserObjectName(desktop) + " heap=" + heapSize;
    }
}
'@

function Format-Gigabyte([double] $Bytes) { return '{0:N1}G' -f ($Bytes / 1GB) }
function Format-Megabyte([double] $Bytes) { return '{0:N0}M' -f ($Bytes / 1MB) }

function Get-SharedSection {
    try {
        $Value = (Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\SubSystems' -Name 'Windows').Windows
        $Match = [regex]::Match([string]$Value, 'SharedSection=[0-9,]+')
        if ($Match.Success) { return $Match.Value }
        return 'SharedSection=unknown'
    }
    catch {
        return "SharedSection=unreadable($($_.Exception.Message))"
    }
}

function Get-PageFileUsage {
    try {
        $Files = @(Get-CimInstance -ClassName Win32_PageFileUsage -OperationTimeoutSec 20)
        if ($Files.Count -eq 0) { return 'none' }
        return ($Files | ForEach-Object { "$($_.Name) allocated=$($_.AllocatedBaseSize)M peak=$($_.PeakUsage)M" }) -join '; '
    }
    catch {
        return "unreadable($($_.Exception.Message))"
    }
}

function Get-Header {
    $Self = [System.Diagnostics.Process]::GetCurrentProcess()
    $Runner = @([System.Diagnostics.Process]::GetProcesses() | Where-Object { $_.ProcessName -like 'Runner.*' } |
            ForEach-Object { "$($_.ProcessName)(pid $($_.Id), session $($_.SessionId))" }) -join ', '
    $Performance = [LeapMuxResourceProbe]::Performance()
    return @(
        "os=$([Environment]::OSVersion.VersionString) image=$($env:ImageOS) $($env:ImageVersion) machine=$([Environment]::MachineName)"
        "processors=$([Environment]::ProcessorCount) physical=$(Format-Gigabyte $Performance[3]) commit-limit=$(Format-Gigabyte $Performance[1])"
        "pagefiles=$(Get-PageFileUsage)"
        "$(Get-SharedSection) desktop=$([LeapMuxResourceProbe]::Desktop()) session=$($Self.SessionId) interactive=$([Environment]::UserInteractive)"
        "runner=$Runner"
    ) -join [Environment]::NewLine
}

$script:LastTimes = $null

function Get-CpuPercent {
    $Times = [LeapMuxResourceProbe]::SystemTimes()
    $Previous = $script:LastTimes
    $script:LastTimes = $Times
    if ($null -eq $Previous) { return 'n/a' }
    $Idle = $Times[0] - $Previous[0]
    $Total = ($Times[1] - $Previous[1]) + ($Times[2] - $Previous[2])
    if ($Total -le 0) { return 'n/a' }
    return '{0:N0}%' -f (100 * (1 - $Idle / $Total))
}

function Invoke-Canary {
    $Watch = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        $StartInfo = [System.Diagnostics.ProcessStartInfo]::new($env:ComSpec, '/d /c exit 0')
        $StartInfo.UseShellExecute = $false
        $StartInfo.CreateNoWindow = $true
        $Canary = [System.Diagnostics.Process]::Start($StartInfo)
        if (-not $Canary.WaitForExit(15000)) {
            $Canary.Kill()
            return 'hung'
        }
        if ($Canary.ExitCode -eq 0) { return "ok($($Watch.ElapsedMilliseconds)ms)" }
        return 'exit=0x{0:X8}({1}ms)' -f $Canary.ExitCode, $Watch.ElapsedMilliseconds
    }
    catch {
        return "fail($($_.Exception.Message))"
    }
}

function Get-TcpStateCount {
    try {
        $Connections = [System.Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpConnections()
        $Groups = $Connections | Group-Object { [string]$_.State } | Sort-Object Count -Descending
        if ($null -eq $Groups) { return 'none' }
        return (@($Groups) | ForEach-Object { "$($_.Name):$($_.Count)" }) -join ','
    }
    catch {
        return "fail($($_.Exception.Message))"
    }
}

function Get-FreeDisk {
    $Parts = foreach ($Drive in [System.IO.DriveInfo]::GetDrives()) {
        if ($Drive.DriveType -ne [System.IO.DriveType]::Fixed -or -not $Drive.IsReady) { continue }
        "$($Drive.Name.TrimEnd('\'))$(Format-Gigabyte $Drive.AvailableFreeSpace)"
    }
    return @($Parts) -join ','
}

# Get-ProcessRow reads one row for each process. A process can exit while
# the monitor reads it, so each read tolerates a failure.
function Get-ProcessRow {
    $Rows = foreach ($Process in [System.Diagnostics.Process]::GetProcesses()) {
        try {
            $Details = [LeapMuxResourceProbe]::ProcessDetails($Process.Id)
            [pscustomobject]@{
                Name    = $Process.ProcessName
                Id      = $Process.Id
                Session = $Process.SessionId
                Private = $Process.PrivateMemorySize64
                Handles = $Process.HandleCount
                Parent  = $Details[0]
                User    = $Details[1]
                Gdi     = $Details[2]
            }
        }
        catch {
            continue
        }
        finally {
            $Process.Dispose()
        }
    }
    return @($Rows)
}

function Get-Sample([int] $Index) {
    $Performance = [LeapMuxResourceProbe]::Performance()
    $Rows = Get-ProcessRow
    $NameById = @{}
    foreach ($Row in $Rows) { $NameById[[long]$Row.Id] = $Row.Name }
    $Count = { param($Name) @($Rows | Where-Object { $_.Name -eq $Name }).Count }
    $Tests = @($Rows | Where-Object { $_.Name -like '*.test' } | ForEach-Object { $_.Name -replace '\.test$', '' } | Sort-Object -Unique) -join ','
    $UserBySession = @($Rows | Where-Object { $_.User -ge 0 } | Group-Object Session | Sort-Object Name |
            ForEach-Object { "s$($_.Name):$(($_.Group | Measure-Object User -Sum).Sum)" }) -join ','
    $Runner = @($Rows | Where-Object { $_.Name -like 'Runner.*' } | ForEach-Object { "$($_.Name -replace '^Runner\.', ''):$(Format-Megabyte $_.Private)" }) -join ','
    if ($Runner.Length -eq 0) { $Runner = 'absent' }
    $Line = @(
        [DateTime]::UtcNow.ToString('HH:mm:ss')
        "cpu=$(Get-CpuPercent)"
        "commit=$(Format-Gigabyte $Performance[0])/$(Format-Gigabyte $Performance[1])"
        "avail=$(Format-Gigabyte $Performance[4])"
        "kpaged=$(Format-Megabyte $Performance[5])"
        "knp=$(Format-Megabyte $Performance[6])"
        "procs=$($Performance[8])"
        "threads=$($Performance[9])"
        "handles=$($Performance[7])"
        "user=$UserBySession"
        "conhost=$(& $Count 'conhost')"
        "pwsh=$(& $Count 'pwsh')"
        "powershell=$(& $Count 'powershell')"
        "cmd=$(& $Count 'cmd')"
        "git=$(& $Count 'git')"
        "tcp=$(Get-TcpStateCount)"
        "free=$(Get-FreeDisk)"
        "canary=$(Invoke-Canary)"
        "runner=$Runner"
        "tests=$Tests"
    ) -join ' '
    $Detail = $null
    if ($Index % $DetailEvery -eq 0) {
        $Parent = { param($Row) if ($NameById.ContainsKey([long]$Row.Parent)) { $NameById[[long]$Row.Parent] } else { "pid$($Row.Parent)" } }
        $ByMemory = $Rows | Sort-Object Private -Descending | Select-Object -First 12 |
            ForEach-Object { '  {0,-28} pid={1,-6} private={2,-6} handles={3,-6} user={4,-5} parent={5}' -f $_.Name, $_.Id, (Format-Megabyte $_.Private), $_.Handles, $_.User, (& $Parent $_) }
        $ByCount = $Rows | Group-Object Name | Sort-Object Count -Descending | Select-Object -First 10 |
            ForEach-Object {
                $Parents = @($_.Group | ForEach-Object { & $Parent $_ } | Group-Object | Sort-Object Count -Descending | Select-Object -First 4 |
                        ForEach-Object { "$($_.Name)x$($_.Count)" }) -join ','
                '  {0,-28} x{1,-4} parents={2}' -f $_.Name, $_.Count, $Parents
            }
        $ByUser = $Rows | Where-Object { $_.User -gt 0 } | Sort-Object User -Descending | Select-Object -First 8 |
            ForEach-Object { '  {0,-28} pid={1,-6} user={2,-5} gdi={3,-5} session={4} parent={5}' -f $_.Name, $_.Id, $_.User, $_.Gdi, $_.Session, (& $Parent $_) }
        $Detail = @('By private memory:') + @($ByMemory) + @('By count:') + @($ByCount) + @('By USER objects:') + @($ByUser) -join [Environment]::NewLine
    }
    return [pscustomobject]@{ Line = $Line; Detail = $Detail }
}

function Publish-Output([string] $CheckRunId, [string] $Title, [string] $Summary, [string] $Text, [bool] $Complete) {
    $Body = @{
        output = @{
            title   = $Title
            summary = Limit-Text $Summary
            text    = Limit-Text $Text
        }
    }
    if ($Complete) {
        $Body.status = 'completed'
        $Body.conclusion = 'neutral'
    }
    Invoke-GitHub -Method 'Patch' -Path "/check-runs/$CheckRunId" -Body $Body | Out-Null
}

function Invoke-Monitor {
    Add-Type -TypeDefinition $ProbeSource -Language CSharp
    $CheckRunId = Read-CheckRunId
    $Header = Get-Header
    Add-MonitorLog $Header
    $Lines = [System.Collections.Generic.List[string]]::new()
    $LastDetail = ''
    $LastPublish = [DateTime]::MinValue
    $Publish = 'pending'
    $Index = 0
    while ($true) {
        $Stopping = Test-Path -LiteralPath $StopPath
        try {
            $Sample = Get-Sample $Index
            $Line = "$($Sample.Line) pub=$Publish"
            Add-MonitorLog $Line
            $Lines.Add($Line)
            if ($null -ne $Sample.Detail) {
                Add-MonitorLog $Sample.Detail
                $LastDetail = $Sample.Detail
            }
        }
        catch {
            $Line = "$([DateTime]::UtcNow.ToString('HH:mm:ss')) sample failed: $($_.Exception.Message)"
            Add-MonitorLog $Line
            $Lines.Add($Line)
        }
        $Index++
        if ($null -ne $CheckRunId -and ($Stopping -or ([DateTime]::UtcNow - $LastPublish).TotalSeconds -ge $PublishSeconds)) {
            $Watch = [System.Diagnostics.Stopwatch]::StartNew()
            try {
                $Fence = '```'
                $Summary = "$Fence`n$Header`n`n$($Lines[$Lines.Count - 1])`n$LastDetail`n$Fence"
                $Text = "$Fence`n$($Lines -join "`n")`n$Fence"
                Publish-Output -CheckRunId $CheckRunId -Title "Sample $Index at $([DateTime]::UtcNow.ToString('HH:mm:ss')) UTC" -Summary $Summary -Text $Text -Complete $Stopping
                $Publish = "ok($($Watch.ElapsedMilliseconds)ms)"
            }
            catch {
                $Publish = "fail($($Watch.ElapsedMilliseconds)ms: $($_.Exception.Message))"
            }
            $LastPublish = [DateTime]::UtcNow
        }
        if ($Stopping) {
            Add-MonitorLog "The monitor ended after $Index samples. Last publish: $Publish"
            return
        }
        Start-Sleep -Seconds $SampleSeconds
    }
}

switch ($Mode) {
    'Start' {
        # Diagnostics must never fail the job, so this mode reports a failure
        # as a warning.
        try {
            Start-Monitor
        }
        catch {
            Write-Warning "The resource monitor did not start: $_"
        }
    }
    'Run' {
        try {
            Invoke-Monitor
        }
        catch {
            Add-MonitorLog "The monitor failed: $($_.Exception.Message)"
            throw
        }
    }
    'Stop' {
        try {
            Stop-Monitor
        }
        catch {
            Write-Warning "The resource monitor did not stop cleanly: $_"
        }
    }
}
