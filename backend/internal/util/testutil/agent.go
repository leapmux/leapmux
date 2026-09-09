package testutil

// AgentCleaner is the subset of agent.Manager a test needs to tear down the
// agent processes a case left running. Defined here so testutil stays
// decoupled from the agent package, exactly as TerminalCleaner is.
type AgentCleaner interface {
	ListAgentIDs() []string
	StopAndWaitAgent(agentID string) bool
}

// StopAllAgents stops every agent the manager still tracks and waits for each
// process to exit.
//
// A spawned agent runs with its tab's WORKING DIRECTORY as its cwd, and that
// directory is almost always a t.TempDir. Nothing else stops these: the service
// harness closes the database and the channel manager, and Shutdown
// deliberately leaves live processes alone -- so without this the CLI outlives
// the case that started it, for the whole test binary.
//
// The symptom is platform-specific, but the LEAK is not:
//
//   - On Windows a process holds an open handle to its cwd, so the TempDir
//     cleanup fails outright with "The process cannot access the file because
//     it is being used by another process" and the case is reported as failed.
//   - On Linux and macOS the same removal SUCCEEDS -- unlink does not care that
//     a process is sitting in the directory -- so the case passes while the
//     agent keeps running in a directory that no longer has a name, holding its
//     descriptors and whatever the provider CLI opened.
//
// So this is not a Windows workaround. Windows is just the platform that
// reports the bug the other two hide.
//
// Waiting is the point, not merely stopping: a signalled-but-unreaped process
// still holds its cwd. StopAndWaitAgent blocks until the manager's exit
// bookkeeping is done, which is the same guarantee RegisterTerminalCleanup gets
// from WaitForExit.
//
// Call this from a DEFER in the test body rather than a t.Cleanup. Deferred
// calls run before every t.Cleanup, so the ordering holds however the case
// happened to interleave its t.TempDir calls with its harness setup -- which is
// what RegisterTerminalCleanup has to solve with a documented LIFO rule
// instead, because it is registered per terminal rather than run per case.
func StopAllAgents(mgr AgentCleaner) {
	if mgr == nil {
		return
	}
	for _, id := range mgr.ListAgentIDs() {
		mgr.StopAndWaitAgent(id)
	}
}
