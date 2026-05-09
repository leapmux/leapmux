package service

// RemoteIPCFactory abstracts the per-agent local-IPC server lifecycle so
// the service layer doesn't depend on the worker's runtime
// (cmd/leapmux/worker.go) directly. Implementations live there and wire
// in the actual remoteipc.Server + crossworker.Client.
type RemoteIPCFactory interface {
	// AgentSpawning is called just before exec. info describes the
	// spawned tab; the implementation creates a per-agent socket and
	// returns env vars to inject + a cleanup func to call on
	// agent close.
	//
	// Returning (nil, nil, nil) is fine when remote control is
	// disabled for this spawn; the caller proceeds without injecting
	// env vars.
	AgentSpawning(info AgentSpawnInfo) (envVars []string, cleanup func(), err error)
	// TerminalSpawning is the terminal counterpart.
	TerminalSpawning(info TerminalSpawnInfo) (envVars []string, cleanup func(), err error)
}

// AgentSpawnInfo identifies a spawning agent so the IPC factory can
// scope its bearer / socket name appropriately. UserID / OrgID /
// WorkspaceID / WorkerID / TabID / WorkingDir / AgentProvider feed
// into LEAPMUX_REMOTE_* env vars (TAB_ID, TAB_TYPE=agent, ORG_ID,
// USER_ID, WORKER_ID, WORKING_DIR, AGENT_PROVIDER) so child CLI
// invocations can default flags. Tile id isn't here — the CLI
// derives it from the tab id at command time via the hub's LocateTab
// RPC.
type AgentSpawnInfo struct {
	UserID        string
	OrgID         string
	WorkspaceID   string
	WorkerID      string
	TabID         string // The spawned agent's id (becomes LEAPMUX_REMOTE_TAB_ID).
	WorkingDir    string
	AgentProvider string
}

// TerminalSpawnInfo identifies a spawning terminal.
type TerminalSpawnInfo struct {
	UserID      string
	OrgID       string
	WorkspaceID string
	WorkerID    string
	TabID       string // The spawned terminal's id (becomes LEAPMUX_REMOTE_TAB_ID).
	WorkingDir  string
}
