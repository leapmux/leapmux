using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Runtime.ExceptionServices;
using System.Runtime.InteropServices;
using System.Runtime.Serialization;
using System.Runtime.Serialization.Json;
using System.Text;
using System.Threading;

[DataContract]
public sealed class LeapMuxJobMember {
    [DataMember(Name = "pid")] public uint Pid;
    [DataMember(Name = "creationTime")] public string CreationTime;
}

[DataContract]
public sealed class LeapMuxJobState {
    [DataMember(Name = "version")] public int Version = 1;
    [DataMember(Name = "ownerPid")] public uint OwnerPid;
    [DataMember(Name = "rootPid")] public uint RootPid;
    [DataMember(Name = "complete")] public bool Complete;
    [DataMember(Name = "members")] public List<LeapMuxJobMember> Members = new List<LeapMuxJobMember>();
}

public static class LeapMuxCommandJob {
    const uint KillOnClose = 0x2000;
    const uint CreateSuspended = 0x4;
    const uint UnicodeEnvironment = 0x400;
    const uint StartupUseStdHandles = 0x100;
    const uint ExtendedStartup = 0x80000;
    const uint QueryLimitedInformation = 0x1000;
    const uint Synchronize = 0x100000;
    const uint EventModifyState = 0x2;
    const uint WaitFailed = 0xffffffff;
    const uint WaitTimeout = 258;
    const uint Infinite = 0xffffffff;
    const uint DuplicateSameAccess = 0x2;

    [StructLayout(LayoutKind.Sequential)] struct BasicLimits {
        public long ProcessUserTime, JobUserTime;
        public uint Flags;
        public UIntPtr MinimumWorkingSet, MaximumWorkingSet;
        public uint ActiveProcessLimit;
        public UIntPtr Affinity;
        public uint Priority, SchedulingClass;
    }
    [StructLayout(LayoutKind.Sequential)] struct IoCounters {
        public ulong ReadOperations, WriteOperations, OtherOperations, ReadBytes, WriteBytes, OtherBytes;
    }
    [StructLayout(LayoutKind.Sequential)] struct ExtendedLimits {
        public BasicLimits Basic;
        public IoCounters Io;
        public UIntPtr ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory;
    }
    [StructLayout(LayoutKind.Sequential)] struct Accounting {
        public long UserTime, KernelTime, PeriodUserTime, PeriodKernelTime;
        public uint PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses;
    }
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)] struct StartupInfo {
        public uint Size;
        public string Reserved, Desktop, Title;
        public uint X, Y, Width, Height, XChars, YChars, FillAttribute, Flags;
        public ushort ShowWindow, ReservedBytes;
        public IntPtr ReservedData, Input, Output, Error;
    }
    [StructLayout(LayoutKind.Sequential)] struct ProcessInformation {
        public IntPtr Process, Thread;
        public uint Pid, ThreadId;
    }
    [StructLayout(LayoutKind.Sequential)] struct ExtendedStartupInfo {
        public StartupInfo Basic;
        public IntPtr Attributes;
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] static extern IntPtr CreateJobObject(IntPtr attributes, string name);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool SetInformationJobObject(IntPtr job, int kind, ref ExtendedLimits data, uint size);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool QueryInformationJobObject(IntPtr job, int kind, IntPtr data, uint size, out uint returned);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool IsProcessInJob(IntPtr process, IntPtr job, out bool result);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool TerminateJobObject(IntPtr job, uint code);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool TerminateProcess(IntPtr process, uint code);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] static extern bool CreateProcess(string executable, StringBuilder commandLine, IntPtr processAttributes, IntPtr threadAttributes, bool inherit, uint flags, IntPtr environment, string directory, ref ExtendedStartupInfo startup, out ProcessInformation process);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool InitializeProcThreadAttributeList(IntPtr attributes, int count, int flags, ref IntPtr size);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool UpdateProcThreadAttribute(IntPtr attributes, uint flags, IntPtr kind, IntPtr value, IntPtr size, IntPtr previous, IntPtr returned);
    [DllImport("kernel32.dll")] static extern void DeleteProcThreadAttributeList(IntPtr attributes);
    [DllImport("kernel32.dll", SetLastError = true)] static extern uint ResumeThread(IntPtr thread);
    [DllImport("kernel32.dll", SetLastError = true)] static extern IntPtr GetStdHandle(int kind);
    [DllImport("kernel32.dll")] static extern IntPtr GetCurrentProcess();
    [DllImport("kernel32.dll")] static extern uint GetCurrentProcessId();
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool DuplicateHandle(IntPtr sourceProcess, IntPtr source, IntPtr targetProcess, out IntPtr target, uint access, bool inherit, uint options);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool CloseHandle(IntPtr handle);
    [DllImport("kernel32.dll", SetLastError = true)] static extern uint WaitForSingleObject(IntPtr handle, uint delay);
    [DllImport("kernel32.dll", SetLastError = true)] static extern uint WaitForMultipleObjects(uint count, IntPtr[] handles, bool all, uint delay);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool GetExitCodeProcess(IntPtr process, out uint code);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool GetProcessTimes(IntPtr process, out long creation, out long exit, out long kernel, out long user);
    [DllImport("kernel32.dll", SetLastError = true)] static extern IntPtr OpenProcess(uint access, bool inherit, uint pid);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] static extern IntPtr CreateEvent(IntPtr attributes, bool manual, bool initial, string name);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] static extern IntPtr OpenEvent(uint access, bool inherit, string name);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool SetEvent(IntPtr handle);

    static Win32Exception NativeError(string operation) {
        return new Win32Exception(Marshal.GetLastWin32Error(), operation + " failed.");
    }

    static void Close(IntPtr handle) {
        if (handle != IntPtr.Zero && handle != new IntPtr(-1) && !CloseHandle(handle))
            throw NativeError("Native handle close");
    }

    static IntPtr DuplicateStandardHandle(int kind) {
        IntPtr source = GetStdHandle(kind), result;
        if (source == IntPtr.Zero || source == new IntPtr(-1))
            throw new InvalidOperationException("The command has no standard stream handle.");
        if (!DuplicateHandle(GetCurrentProcess(), source, GetCurrentProcess(), out result, 0, true, DuplicateSameAccess))
            throw NativeError("Standard stream handle duplication");
        return result;
    }

    static string CreationTime(IntPtr handle) {
        long creation, exit, kernel, user;
        if (!GetProcessTimes(handle, out creation, out exit, out kernel, out user))
            throw NativeError("Process creation time query");
        return creation.ToString(System.Globalization.CultureInfo.InvariantCulture);
    }

    static uint ActiveProcesses(IntPtr job) {
        int size = Marshal.SizeOf(typeof(Accounting));
        IntPtr buffer = Marshal.AllocHGlobal(size);
        try {
            uint returned;
            if (!QueryInformationJobObject(job, 1, buffer, (uint)size, out returned))
                throw NativeError("Job accounting query");
            Accounting accounting = (Accounting)Marshal.PtrToStructure(buffer, typeof(Accounting));
            return accounting.ActiveProcesses;
        }
        finally { Marshal.FreeHGlobal(buffer); }
    }

    static List<LeapMuxJobMember> Members(IntPtr job) {
        int capacity = 32;
        while (true) {
            int size = checked(8 + IntPtr.Size * capacity);
            IntPtr buffer = Marshal.AllocHGlobal(size);
            try {
                uint returned;
                if (!QueryInformationJobObject(job, 3, buffer, (uint)size, out returned)) {
                    if (Marshal.GetLastWin32Error() == 234 && capacity < 1048576) {
                        capacity = checked(capacity * 2);
                        continue;
                    }
                    throw NativeError("Job process list query");
                }
                int count = Marshal.ReadInt32(buffer, 4);
                if (count < 0 || count > capacity)
                    throw new InvalidOperationException("The job process list has an invalid size.");
                List<LeapMuxJobMember> result = new List<LeapMuxJobMember>();
                for (int index = 0; index < count; index++) {
                    uint pid = checked((uint)Marshal.ReadIntPtr(buffer, 8 + index * IntPtr.Size).ToInt64());
                    IntPtr process = OpenProcess(QueryLimitedInformation | Synchronize, false, pid);
                    if (process == IntPtr.Zero) {
                        int error = Marshal.GetLastWin32Error();
                        if (error == 87 || error == 1168) continue;
                        throw new Win32Exception(error, "Job member handle open failed.");
                    }
                    try {
                        bool belongs;
                        if (!IsProcessInJob(process, job, out belongs)) throw NativeError("Job member ownership query");
                        if (belongs)
                            result.Add(new LeapMuxJobMember { Pid = pid, CreationTime = CreationTime(process) });
                    }
                    finally { Close(process); }
                }
                return result;
            }
            finally { Marshal.FreeHGlobal(buffer); }
        }
    }

    static void WriteState(string path, LeapMuxJobState state) {
        string draft = path + ".writing";
        using (FileStream stream = new FileStream(draft, FileMode.Create, FileAccess.Write, FileShare.None)) {
            new DataContractJsonSerializer(typeof(LeapMuxJobState)).WriteObject(stream, state);
            stream.Flush(true);
        }
        if (File.Exists(path)) File.Replace(draft, path, null);
        else File.Move(draft, path);
    }

    static void WaitForEmptyJob(IntPtr job, uint delay) {
        Stopwatch elapsed = Stopwatch.StartNew();
        while (ActiveProcesses(job) != 0) {
            if (elapsed.ElapsedMilliseconds >= delay)
                throw new TimeoutException("The Windows job did not exit before the shutdown deadline.");
            Thread.Sleep(25);
        }
    }

    static void Cleanup(Action operation, List<Exception> failures) {
        try { operation(); }
        catch (Exception error) { failures.Add(error); }
    }

    public static string BuildCommandLine(string[] arguments, bool verbatim) {
        if (arguments == null || arguments.Length == 0) throw new ArgumentException("The command has no argument list.");
        StringBuilder result = new StringBuilder();
        bool first = true;
        foreach (string argument in arguments) {
            if (argument == null || argument.IndexOf('\0') >= 0) throw new ArgumentException("The command argument is null or contains NUL.");
            if (!first) result.Append(' ');
            first = false;
            if (verbatim || (argument.Length != 0 && argument.IndexOfAny(new char[] { ' ', '\t', '"' }) < 0)) {
                result.Append(argument);
                continue;
            }
            result.Append('"');
            int slashes = 0;
            foreach (char character in argument) {
                if (character == '\\') { slashes++; continue; }
                if (character == '"') result.Append('\\', checked(slashes * 2 + 1)).Append('"');
                else result.Append('\\', slashes).Append(character);
                slashes = 0;
            }
            result.Append('\\', checked(slashes * 2)).Append('"');
        }
        if (result.Length >= 32767) throw new ArgumentException("The native command line has an invalid size.");
        return result.ToString();
    }

    public static void ValidatePayload(string executable, string directory, string environment, string statePath, string stopEventName, uint delay) {
        foreach (string value in new string[] { executable, directory, statePath, stopEventName }) {
            if (String.IsNullOrEmpty(value) || value.IndexOf('\0') >= 0) throw new ArgumentException("The native command payload contains an empty value or NUL.");
        }
        if (environment == null || !environment.EndsWith("\0\0", StringComparison.Ordinal)) throw new ArgumentException("The native environment block has no complete ending.");
        if (delay == 0 || delay > 2147483647) throw new ArgumentException("The native shutdown delay has an invalid value.");
    }

    public static int Run(string executable, string[] arguments, bool verbatim, string directory, string environment, string statePath, string stopEventName, uint delay) {
        ValidatePayload(executable, directory, environment, statePath, stopEventName, delay);
        string commandLine = BuildCommandLine(arguments, verbatim);
        IntPtr job = IntPtr.Zero, stopEvent = IntPtr.Zero, environmentMemory = IntPtr.Zero;
        ExtendedStartupInfo startup = new ExtendedStartupInfo();
        IntPtr jobAttribute = IntPtr.Zero, streamAttribute = IntPtr.Zero;
        bool attributesInitialized = false;
        ProcessInformation root = new ProcessInformation();
        bool assigned = false;
        Exception operationFailure = null;
        List<Exception> cleanupFailures = new List<Exception>();
        uint code = 1;
        try {
            job = CreateJobObject(IntPtr.Zero, null);
            if (job == IntPtr.Zero) throw NativeError("Job creation");
            ExtendedLimits limits = new ExtendedLimits();
            limits.Basic.Flags = KillOnClose;
            if (!SetInformationJobObject(job, 9, ref limits, (uint)Marshal.SizeOf(typeof(ExtendedLimits))))
                throw NativeError("Job shutdown policy");
            stopEvent = CreateEvent(IntPtr.Zero, true, false, stopEventName);
            if (stopEvent == IntPtr.Zero) throw NativeError("Shutdown event creation");
            if (Marshal.GetLastWin32Error() == 183)
                throw new InvalidOperationException("The private shutdown event already exists.");
            startup.Basic.Size = (uint)Marshal.SizeOf(typeof(ExtendedStartupInfo));
            startup.Basic.Flags = StartupUseStdHandles;
            startup.Basic.Input = DuplicateStandardHandle(-10);
            startup.Basic.Output = DuplicateStandardHandle(-11);
            startup.Basic.Error = DuplicateStandardHandle(-12);
            IntPtr attributeSize = IntPtr.Zero;
            InitializeProcThreadAttributeList(IntPtr.Zero, 2, 0, ref attributeSize);
            if (attributeSize == IntPtr.Zero) throw NativeError("Process attribute size query");
            startup.Attributes = Marshal.AllocHGlobal(attributeSize);
            if (!InitializeProcThreadAttributeList(startup.Attributes, 2, 0, ref attributeSize)) throw NativeError("Process attribute initialization");
            attributesInitialized = true;
            jobAttribute = Marshal.AllocHGlobal(IntPtr.Size);
            Marshal.WriteIntPtr(jobAttribute, job);
            // Assign the job during creation. A later assignment can leave a suspended child outside the job when its owner stops.
            if (!UpdateProcThreadAttribute(startup.Attributes, 0, new IntPtr(0x2000d), jobAttribute, new IntPtr(IntPtr.Size), IntPtr.Zero, IntPtr.Zero))
                throw NativeError("Atomic process job assignment");
            streamAttribute = Marshal.AllocHGlobal(3 * IntPtr.Size);
            Marshal.WriteIntPtr(streamAttribute, 0, startup.Basic.Input);
            Marshal.WriteIntPtr(streamAttribute, IntPtr.Size, startup.Basic.Output);
            Marshal.WriteIntPtr(streamAttribute, 2 * IntPtr.Size, startup.Basic.Error);
            if (!UpdateProcThreadAttribute(startup.Attributes, 0, new IntPtr(0x20002), streamAttribute, new IntPtr(3 * IntPtr.Size), IntPtr.Zero, IntPtr.Zero))
                throw NativeError("Standard stream inheritance policy");
            environmentMemory = Marshal.StringToHGlobalUni(environment);
            if (!CreateProcess(executable, new StringBuilder(commandLine), IntPtr.Zero, IntPtr.Zero, true, CreateSuspended | UnicodeEnvironment | ExtendedStartup, environmentMemory, directory, ref startup, out root))
                throw NativeError("Suspended command creation");
            bool belongs;
            if (!IsProcessInJob(root.Process, job, out belongs)) throw NativeError("Suspended command job verification");
            if (!belongs) throw new InvalidOperationException("The suspended command does not belong to its private job.");
            assigned = true;
            LeapMuxJobState state = new LeapMuxJobState { OwnerPid = GetCurrentProcessId(), RootPid = root.Pid, Members = Members(job) };
            WriteState(statePath, state);
            uint reason;
            uint stopState = WaitForSingleObject(stopEvent, 0);
            if (stopState == WaitFailed) throw NativeError("Pre-resume shutdown event wait");
            if (stopState != 0 && stopState != WaitTimeout) throw new InvalidOperationException("The pre-resume event wait returned an invalid result.");
            if (stopState == 0) reason = 1;
            else {
                if (ResumeThread(root.Thread) == WaitFailed) throw NativeError("Command thread resume");
                reason = WaitForMultipleObjects(2, new IntPtr[] { root.Process, stopEvent }, false, Infinite);
            }
            if (reason == WaitFailed) throw NativeError("Command or shutdown event wait");
            if (reason != 0 && reason != 1) throw new InvalidOperationException("The native command wait returned an invalid result.");
            if (reason == 0 && !GetExitCodeProcess(root.Process, out code)) throw NativeError("Command exit code query");
            state.Members = Members(job);
            if (!state.Members.Exists(delegate(LeapMuxJobMember member) { return member.Pid == root.Pid; }))
                state.Members.Add(new LeapMuxJobMember { Pid = root.Pid, CreationTime = CreationTime(root.Process) });
            WriteState(statePath, state);
            if (!TerminateJobObject(job, 1)) throw NativeError("Remaining job member termination");
            WaitForEmptyJob(job, delay);
            state.Complete = true;
            WriteState(statePath, state);
        }
        catch (Exception error) { operationFailure = error; }
        finally {
            if (root.Process != IntPtr.Zero && !assigned) {
                Cleanup(delegate { if (!TerminateProcess(root.Process, 1)) throw NativeError("Suspended command termination"); }, cleanupFailures);
                Cleanup(delegate { if (WaitForSingleObject(root.Process, delay) != 0) throw new TimeoutException("The suspended command did not exit."); }, cleanupFailures);
            }
            if (job != IntPtr.Zero) {
                Cleanup(delegate { if (!TerminateJobObject(job, 1)) throw NativeError("Job cleanup termination"); }, cleanupFailures);
                Cleanup(delegate { WaitForEmptyJob(job, delay); }, cleanupFailures);
            }
            Cleanup(delegate { Close(root.Thread); }, cleanupFailures);
            Cleanup(delegate { Close(root.Process); }, cleanupFailures);
            if (attributesInitialized) DeleteProcThreadAttributeList(startup.Attributes);
            if (startup.Attributes != IntPtr.Zero) Marshal.FreeHGlobal(startup.Attributes);
            if (jobAttribute != IntPtr.Zero) Marshal.FreeHGlobal(jobAttribute);
            if (streamAttribute != IntPtr.Zero) Marshal.FreeHGlobal(streamAttribute);
            Cleanup(delegate { Close(startup.Basic.Input); }, cleanupFailures);
            Cleanup(delegate { Close(startup.Basic.Output); }, cleanupFailures);
            Cleanup(delegate { Close(startup.Basic.Error); }, cleanupFailures);
            Cleanup(delegate { Close(stopEvent); }, cleanupFailures);
            Cleanup(delegate { Close(job); }, cleanupFailures);
            if (environmentMemory != IntPtr.Zero) Marshal.FreeHGlobal(environmentMemory);
        }
        if (operationFailure != null) {
            if (cleanupFailures.Count != 0) {
                cleanupFailures.Insert(0, operationFailure);
                throw new AggregateException("The command and its native cleanup failed.", cleanupFailures);
            }
            ExceptionDispatchInfo.Capture(operationFailure).Throw();
        }
        if (cleanupFailures.Count != 0) throw new AggregateException("The native command cleanup failed.", cleanupFailures);
        return unchecked((int)code);
    }

    public static bool RequestStop(string eventName) {
        IntPtr handle = OpenEvent(EventModifyState, false, eventName);
        if (handle == IntPtr.Zero) {
            if (Marshal.GetLastWin32Error() == 2) return false;
            throw NativeError("Private shutdown event open");
        }
        try {
            if (!SetEvent(handle)) throw NativeError("Private shutdown event signal");
            return true;
        }
        finally { Close(handle); }
    }

    public static string SnapshotCreationTime(uint pid) {
        IntPtr handle = OpenProcess(QueryLimitedInformation, false, pid);
        if (handle == IntPtr.Zero) {
            int error = Marshal.GetLastWin32Error();
            if (error == 5 || error == 87 || error == 1168) return null;
            throw new Win32Exception(error, "Process creation handle open failed.");
        }
        try { return CreationTime(handle); }
        finally { Close(handle); }
    }

    public static void Verify(uint[] pids, string[] creationTimes, uint delay) {
        if (pids.Length != creationTimes.Length) throw new InvalidOperationException("The process identity arrays have different sizes.");
        Stopwatch elapsed = Stopwatch.StartNew();
        for (int index = 0; index < pids.Length; index++) {
            IntPtr process = OpenProcess(QueryLimitedInformation | Synchronize, false, pids[index]);
            if (process == IntPtr.Zero) {
                int error = Marshal.GetLastWin32Error();
                if (error == 87 || error == 1168) continue;
                throw new Win32Exception(error, "Captured process handle open failed.");
            }
            try {
                if (CreationTime(process) != creationTimes[index]) continue;
                uint remaining = checked((uint)Math.Max(0, delay - elapsed.ElapsedMilliseconds));
                uint result = WaitForSingleObject(process, remaining);
                if (result == WaitFailed) throw NativeError("Captured process exit wait");
                if (result == WaitTimeout) throw new TimeoutException("A captured Windows process did not exit before the deadline.");
                if (result != 0) throw new InvalidOperationException("The captured process wait returned an invalid result.");
            }
            finally { Close(process); }
        }
    }
}
