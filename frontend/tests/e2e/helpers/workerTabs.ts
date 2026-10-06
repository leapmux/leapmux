import type { AgentServer } from './workspace'
import { expect } from '@playwright/test'
import {
  AgentStatus,
  CloseAgentRequestSchema,
  CloseAgentResponseSchema,
  ListAgentsRequestSchema,
  ListAgentsResponseSchema,
} from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WorktreeAction } from '../../../src/generated/proto/leapmux/v1/common_pb'
import {
  InspectLastTabCloseRequestSchema,
  InspectLastTabCloseResponseSchema,
  PushBranchRequestSchema,
  PushBranchResponseSchema,
} from '../../../src/generated/proto/leapmux/v1/git_pb'
import {
  CloseTerminalRequestSchema,
  CloseTerminalResponseSchema,
  ListTerminalsRequestSchema,
  ListTerminalsResponseSchema,
} from '../../../src/generated/proto/leapmux/v1/terminal_pb'
import { API_POLL_INTERVAL_MS, callHub, getTestChannel } from './api'
import { retryUntilPass } from './retryUntilPass'
import { waitTimeoutBeforeTestDeadline } from './testDeadline'

/*
 * The Worker RPCs that read, close, and inspect the tabs of a workspace, through
 * the E2EE channel. A Worker RPC takes tab IDs, so each list first reads the
 * workspace's tabs from the Hub.
 */

/**
 * Wait until the workspace holds `expectedCount` agents and none is still
 * AGENT_STATUS_STARTING.
 *
 * `waitForAgentsViaAPI` waits only for an agent to APPEAR, which OpenAgent
 * satisfies as soon as the DB row exists. The git-mode work (creating a worktree,
 * checking a branch out) and the `worktree_tabs` registration both happen on the
 * async startup goroutine AFTER that. So a test that acts on those effects --
 * reading the branch off disk, or closing a sibling tab and expecting the
 * worktree to still be referenced -- has to wait for startup, not for arrival.
 * Reading immediately is a race that fails more often than it passes.
 */
export async function waitForAgentStartupViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
  expectedCount = 1,
  timeoutMs = 30_000,
  intervalMs = API_POLL_INTERVAL_MS,
): Promise<Array<{ id: string, title: string, workingDir: string, status: number, startupError: string }>> {
  const deadline = Date.now() + timeoutMs
  const reads: AgentListReads = {}
  while (true) {
    const agents = await listAgentsForWait(hubUrl, token, workerId, workspaceId, reads)
    // A FAILED startup is terminal, so waiting longer cannot help -- and it is
    // the interesting case: the git-mode work is what failed, so every
    // downstream assertion (the worktree exists, the branch is checked out)
    // would report a confusing false instead of the worker's actual error.
    const failed = agents.filter(a => a.status === AgentStatus.STARTUP_FAILED)
    if (failed.length > 0) {
      throw new Error(
        `waitForAgentStartupViaAPI: agent startup failed: ${failed.map(a => `${a.id}: ${a.startupError || '(no startup_error reported)'}`).join('; ')}`,
      )
    }
    if (agents.length >= expectedCount && agents.every(a => a.status !== AgentStatus.STARTING)) {
      return agents
    }
    if (Date.now() >= deadline) {
      throw new Error(
        `waitForAgentStartupViaAPI: ${expectedCount} agent(s) did not finish starting within ${timeoutMs}ms `
        + `(saw ${JSON.stringify(agents)})${lastFailedRead(reads)}`,
      )
    }
    await new Promise(r => setTimeout(r, intervalMs))
  }
}

/**
 * Close a terminal via E2EE channel. Pass `worktreeAction` to atomically
 * remove the worktree after the PTY/DB cleanup (REMOVE) or keep it (KEEP).
 */
export async function closeTerminalViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  terminalId: string,
  worktreeAction: WorktreeAction = WorktreeAction.KEEP,
): Promise<{ worktreePath: string, worktreeId: string, failureMessage: string, failureDetail: string }> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'CloseTerminal',
    CloseTerminalRequestSchema,
    CloseTerminalResponseSchema,
    { terminalId, worktreeAction },
  )
  const result = resp.result
  return {
    worktreePath: result?.worktreePath ?? '',
    worktreeId: result?.worktreeId ?? '',
    failureMessage: result?.failureMessage ?? '',
    failureDetail: result?.failureDetail ?? '',
  }
}

/**
 * Close an agent via E2EE channel. Pass `worktreeAction` to atomically
 * remove the worktree after the process/DB cleanup (REMOVE) or keep it
 * (KEEP).
 */
export async function closeAgentViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  agentId: string,
  worktreeAction: WorktreeAction = WorktreeAction.KEEP,
): Promise<{ worktreePath: string, worktreeId: string, failureMessage: string, failureDetail: string }> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'CloseAgent',
    CloseAgentRequestSchema,
    CloseAgentResponseSchema,
    { agentId, worktreeAction },
  )
  const result = resp.result
  return {
    worktreePath: result?.worktreePath ?? '',
    worktreeId: result?.worktreeId ?? '',
    failureMessage: result?.failureMessage ?? '',
    failureDetail: result?.failureDetail ?? '',
  }
}

/**
 * Poll `listAgentsViaAPI` until at least one agent is returned or the
 * timeout elapses.  Call this instead of `listAgentsViaAPI` directly when
 * the agent was just created via the UI or an API call that may not have
 * been persisted by the backend yet.
 */
export async function waitForAgentsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
  timeoutMs = 15_000,
  intervalMs = API_POLL_INTERVAL_MS,
): Promise<Array<{ id: string, title: string, workingDir: string, status: number, startupError: string }>> {
  const deadline = Date.now() + timeoutMs
  const reads: AgentListReads = {}
  while (true) {
    const agents = await listAgentsForWait(hubUrl, token, workerId, workspaceId, reads)
    if (agents.length > 0) {
      return agents
    }
    if (Date.now() >= deadline) {
      throw new Error(`No agents appeared for workspace ${workspaceId} within ${timeoutMs}ms${lastFailedRead(reads)}`)
    }
    await new Promise(r => setTimeout(r, intervalMs))
  }
}

/** The last read of a wait loop over `listAgentsViaAPI` that threw. */
interface AgentListReads {
  lastFailure?: unknown
}

/**
 * Read the agents of the workspace for a wait loop. The Hub read and the channel read throw while the Hub or the Worker
 * restarts, so a read that throws reads as no agent, and `reads` keeps the error for the message of a timeout.
 */
async function listAgentsForWait(hubUrl: string, token: string, workerId: string, workspaceId: string, reads: AgentListReads) {
  try {
    return await listAgentsViaAPI(hubUrl, token, workerId, workspaceId)
  }
  catch (error) {
    reads.lastFailure = error
    return []
  }
}

/** State the last read that threw, as the tail of a timeout message, or '' when no read threw. */
function lastFailedRead(reads: AgentListReads): string {
  if (reads.lastFailure === undefined)
    return ''
  return `; the last read that failed: ${reads.lastFailure instanceof Error ? reads.lastFailure.message : String(reads.lastFailure)}`
}

/**
 * Read the IDs of the workspace's tabs of one type from the hub's ListTabs.
 * The hub's list is the first half of every Worker read below: a Worker RPC takes tab IDs, not a workspace ID.
 */
async function workspaceTabIdsViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
  tabType: 'TAB_TYPE_AGENT' | 'TAB_TYPE_TERMINAL',
): Promise<string[]> {
  const data = await callHub<{ tabs?: Array<{ tabType: string, tabId: string }> }>(
    hubUrl,
    'WorkspaceService/ListTabs',
    { workspaceIds: [workspaceId] },
    { cookie: token, operation: `workspaceTabIdsViaAPI(${workspaceId})` },
  )
  return (data.tabs ?? []).filter(tab => tab.tabType === tabType).map(tab => tab.tabId)
}

/**
 * List agents for a workspace via hub ListTabs + worker ListAgents.
 * The ListAgents RPC now accepts tab_ids instead of workspace_id,
 * so we first fetch the tab list from the hub and then request agents by ID.
 */
export async function listAgentsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
): Promise<Array<{ id: string, title: string, workingDir: string, status: number, startupError: string }>> {
  const agentTabIds = await workspaceTabIdsViaAPI(hubUrl, token, workspaceId, 'TAB_TYPE_AGENT')
  if (agentTabIds.length === 0) {
    return []
  }

  const channel = await getTestChannel(hubUrl, token)
  let resp: Awaited<ReturnType<typeof channel.callWorker<typeof ListAgentsRequestSchema, typeof ListAgentsResponseSchema>>>
  try {
    resp = await channel.callWorker(
      workerId,
      'ListAgents',
      ListAgentsRequestSchema,
      ListAgentsResponseSchema,
      { tabIds: agentTabIds },
    )
  }
  catch {
    // Treat as transient; caller retries via waitForAgentsViaAPI.
    return []
  }
  return (resp.agents ?? []).map(a => ({ id: a.id, title: a.title, workingDir: a.workingDir, status: a.status, startupError: a.startupError }))
}

/**
 * The status of one agent of the workspace, as its Worker reports it, or undefined while the Worker lists no such
 * agent. The Worker, not the tab bar, is the authority on an agent's state. The Hub read of the tab list throws when
 * it fails, so a wait on this read uses `waitForAgentStatusViaAPI`, not `expect.poll`.
 */
export async function agentStatusViaAPI(server: AgentServer, workspaceId: string, agentId: string): Promise<number | undefined> {
  const agents = await listAgentsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
  return agents.find(agent => agent.id === agentId)?.status
}

/** Wait until the Worker reports `status` for one agent of the workspace. A read that throws starts the next read. */
export async function waitForAgentStatusViaAPI(server: AgentServer, workspaceId: string, agentId: string, status: AgentStatus): Promise<void> {
  await retryUntilPass(async () => {
    expect(await agentStatusViaAPI(server, workspaceId, agentId), `the Worker reports agent ${agentId} as ${AgentStatus[status]}`).toBe(status)
  })
}

/**
 * Whether the Worker reports one terminal of the workspace as exited, or undefined while it lists no such terminal.
 * A wait on this read uses `waitForTerminalExitViaAPI`, as `agentStatusViaAPI` explains.
 */
export async function terminalExitedViaAPI(server: AgentServer, workspaceId: string, terminalId: string): Promise<boolean | undefined> {
  const terminals = await listTerminalsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
  return terminals.find(terminal => terminal.id === terminalId)?.exited
}

/** Wait until the Worker reports one terminal of the workspace as exited. A read that throws starts the next read. */
export async function waitForTerminalExitViaAPI(server: AgentServer, workspaceId: string, terminalId: string): Promise<void> {
  await retryUntilPass(async () => {
    expect(await terminalExitedViaAPI(server, workspaceId, terminalId), `the Worker reports terminal ${terminalId} as exited`).toBe(true)
  })
}

/**
 * Wait until the workspace holds exactly one agent on its Worker, and return it.
 * More than one agent fails at once, because a test that reads "the" agent would then read an arbitrary one.
 */
export async function waitForSoleAgentViaAPI(
  server: AgentServer,
  workspaceId: string,
): Promise<{ id: string, title: string, workingDir: string, status: number, startupError: string }> {
  const agents = await waitForAgentsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
  const [agent, ...others] = agents
  if (!agent || others.length > 0)
    throw new Error(`Workspace ${workspaceId} must hold exactly one agent, but its Worker lists ${agents.length}: ${agents.map(item => item.id).join(', ')}.`)
  return agent
}

/**
 * Wait until one of the titles that `list` returns equals `title`, and state `message` on a timeout.
 * The Worker's database is the one durable home of a tab title, so `list` reads the Worker, for example through
 * `listTerminalsViaAPI`. A read that throws counts as a miss, and the wait reads again. A timeout reports the last
 * failure: the failed read, or the titles that the last read returned.
 */
export async function waitForWorkerTabTitle(
  list: () => Promise<ReadonlyArray<{ title: string }>>,
  title: string,
  message: string,
): Promise<void> {
  if (title === '')
    throw new Error('A stored tab title check needs a title, because a tab with no title would match an empty one.')
  // `expect.poll` cannot retry a read here: it ends at the first read that throws, and only a failed match starts its
  // next attempt. `toPass` retries the read and the match together.
  await expect(async () => {
    expect((await list()).map(tab => tab.title), message).toContain(title)
  }).toPass({ timeout: waitTimeoutBeforeTestDeadline() })
}

/**
 * List a workspace's terminals via hub ListTabs + worker ListTerminals.
 *
 * The worker's DB is the only durable home of a terminal's title, so this is
 * where a test asks whether a rename PERSISTED. The tab bar shows a rename
 * immediately -- the handler patches local metadata and fires
 * `UpdateTerminalTitle` without awaiting it -- so a reload or a worker restart
 * begun in that window drops the write and the failure reads as a
 * persistence regression. Sibling of `listAgentsViaAPI`, same two-step shape.
 */
export async function listTerminalsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
): Promise<Array<{ id: string, title: string, status: number, exited: boolean }>> {
  const terminalTabIds = await workspaceTabIdsViaAPI(hubUrl, token, workspaceId, 'TAB_TYPE_TERMINAL')
  if (terminalTabIds.length === 0) {
    return []
  }

  const channel = await getTestChannel(hubUrl, token)
  try {
    const resp = await channel.callWorker(
      workerId,
      'ListTerminals',
      ListTerminalsRequestSchema,
      ListTerminalsResponseSchema,
      { tabIds: terminalTabIds },
    )
    return (resp.terminals ?? []).map(t => ({
      id: t.terminalId,
      title: t.title,
      status: t.status,
      exited: t.exited,
    }))
  }
  catch {
    // Treat as transient so a caller polling this converges instead of
    // failing on one blip.
    return []
  }
}

/**
 * Inspect the last-tab close state via E2EE channel.
 */
export async function inspectLastTabCloseViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  tabType: number,
  tabId: string,
): Promise<{
  target: number
  shouldPrompt: boolean
  worktreePath: string
  worktreeId: string
  branchName: string
  canPush: boolean
  hasUncommittedChanges: boolean
  unpushedCommitCount: number
  remoteBranchMissing: boolean
}> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'InspectLastTabClose',
    InspectLastTabCloseRequestSchema,
    InspectLastTabCloseResponseSchema,
    { tabType, tabId },
  )
  const gs = resp.gitState
  return {
    target: resp.target,
    shouldPrompt: resp.shouldPrompt,
    worktreePath: resp.worktreePath,
    worktreeId: resp.worktreeId,
    branchName: resp.branchName,
    canPush: gs?.canPush ?? false,
    hasUncommittedChanges: gs?.hasUncommittedChanges ?? false,
    unpushedCommitCount: gs?.unpushedCommitCount ?? 0,
    remoteBranchMissing: gs?.remoteBranchMissing ?? false,
  }
}

/**
 * Push or commit-and-push the branch a tab lives on, via E2EE channel.
 */
export async function pushBranchViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workingDir: string,
): Promise<void> {
  const channel = await getTestChannel(hubUrl, token)
  await channel.callWorker(
    workerId,
    'PushBranch',
    PushBranchRequestSchema,
    PushBranchResponseSchema,
    { workingDir },
  )
}
