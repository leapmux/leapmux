import type { Page, TestInfo } from '@playwright/test'
import type { ChildProcess } from 'node:child_process'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { AgentOpenOverrides } from '../agentSettings'
import { agentOpenOptions } from '../agentSettings'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './api'
import { withCleanup } from './cleanup'
import { createTestDirectory } from './runDirectory'
import { loginViaToken, openWorkspace } from './ui'

export interface WorkspaceFixture {
  workspaceId: string
  /**
   * The agent's working directory, when the fixture opened an agent.
   *
   * A tool-result image test writes a PNG here so the provider's own read tool
   * can open it. A workspace-only fixture has none.
   */
  workingDir?: string
}

/** A workspace fixture with the one agent that the fixture opened in it. */
export interface AgentWorkspaceFixture extends WorkspaceFixture {
  agentId: string
}

/**
 * The value of a workspace fixture after it opened `agentId` in `workingDir`.
 * An undefined `workingDir` means the Worker's default directory, which the test does not know, so the field stays absent.
 */
export function agentWorkspaceFixture(workspace: WorkspaceFixture, agentId: string, workingDir: string | undefined): AgentWorkspaceFixture {
  return {
    workspaceId: workspace.workspaceId,
    agentId,
    ...(workingDir === undefined ? {} : { workingDir }),
  }
}

/** A workspace with one agent of a provider, and the directory that the agent works in. */
export interface AgentWorkspace {
  workspaceId: string
  workingDir: string
}

/** The hub and the Worker where a test opens an agent. */
export interface AgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * The hub where a test creates a workspace. A workspace needs no Worker, so a hub with several Workers also fits.
 * The hub process, when the test owns one, tells the cleanup whether the hub can still answer.
 */
export interface WorkspaceHub {
  hubUrl: string
  adminToken: string
  hubProc?: ChildProcess
  serverProc?: ChildProcess
}

type WorkspaceServer = WorkspaceHub & AgentServer

/**
 * How the agents of one provider open in the E2E suite.
 * A provider fixture file states it once, and both its workspace fixtures and `openProviderAgent` read it.
 */
export interface ProviderAgent {
  provider: AgentProvider
  /** The prefix of the workspace name and of the default working directory. */
  prefix: string
  /**
   * Create the agent's working directory. Omit it for a fresh private directory of the run, which suits every
   * provider that reads no configuration from the git repository around it.
   */
  workingDir?: () => string
}

/** The working directory of a new agent of `agent`. */
function newWorkingDir(agent: ProviderAgent): string {
  return agent.workingDir?.() ?? createTestDirectory(`${agent.prefix}-wd-`)
}

/** Keep workspace creation and disposal identical across agent providers. */
export async function withTestWorkspace(
  server: WorkspaceHub,
  prefix: string,
  use: (workspace: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  const workspaceId = await createWorkspaceViaAPI(server.hubUrl, server.adminToken, `${prefix}-${crypto.randomUUID()}`)
  await withCleanup(() => use({ workspaceId }), async () => {
    const hub = server.hubProc ?? server.serverProc
    // A stopped hub cannot answer cleanup. The run owns and removes its database directory.
    if (!hub || (hub.exitCode === null && hub.signalCode === null))
      await deleteWorkspaceViaAPI(server.hubUrl, server.adminToken, workspaceId)
  })
}

/**
 * Open a real agent in a private working directory and close it after use.
 * The open request applies the one merge rule of `agentOpenOptions` to `openOptions`.
 */
export async function withAgentWorkspace(
  server: WorkspaceServer,
  options: ProviderAgent & { openOptions?: AgentOpenOverrides },
  use: (workspace: AgentWorkspace) => Promise<void>,
): Promise<void> {
  // Build the request first, so open options that the merge rule refuses create no workspace.
  const request = agentOpenOptions(options.provider, options.openOptions)
  await withTestWorkspace(server, options.prefix, async (workspace) => {
    const workingDir = newWorkingDir(options)
    await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspace.workspaceId, workingDir, request)
    await use({ workspaceId: workspace.workspaceId, workingDir })
  })
}

/** A workspace that `createWorkspaceWithAgentsViaAPI` created, with its agents in the order that they opened. */
export interface WorkspaceWithAgents {
  workspaceId: string
  agentIds: string[]
}

/**
 * Create a workspace on the suite hub with `agentCount` agents, and return the workspace and the agents in open order.
 * The agents open one after another with the provider default, so their tabs keep that order.
 *
 * A spec on the suite hub deletes nothing afterwards: the per-test reset of `./fixtures.ts` deletes every workspace
 * before the next test and reports a failed delete. A spec on its own hub uses `withTestWorkspace` instead.
 */
export async function createWorkspaceWithAgentsViaAPI(
  server: AgentServer,
  title: string,
  options: { agentCount?: number, workingDir?: string } = {},
): Promise<WorkspaceWithAgents> {
  const agentCount = options.agentCount ?? 1
  if (!Number.isSafeInteger(agentCount) || agentCount < 0)
    throw new RangeError(`An agent count must be a nonnegative integer, not ${agentCount}.`)
  const workspaceId = await createWorkspaceViaAPI(server.hubUrl, server.adminToken, title)
  const agentIds: string[] = []
  for (let index = 0; index < agentCount; index++)
    agentIds.push(await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, options.workingDir))
  return { workspaceId, agentIds }
}

/** What a test states for one more agent that it opens in an existing workspace. */
export interface ProviderAgentOpenOptions extends AgentOpenOverrides {
  /** The directory that the agent works in. The default is a new directory of the provider. */
  workingDir?: string
  /** Write into the working directory before the agent starts, for a configuration that the agent reads at its start. */
  prepare?: (workingDir: string) => void
}

/**
 * Open one more agent of a provider in an existing workspace, with the pinned settings and the test's overrides.
 * The open request applies the one merge rule of `agentOpenOptions`.
 */
export async function openProviderAgent(
  server: AgentServer,
  workspaceId: string,
  agent: ProviderAgent,
  options: ProviderAgentOpenOptions = {},
): Promise<{ agentId: string, workingDir: string }> {
  // Build the request first, so an override that the merge rule refuses creates and prepares no directory.
  const request = agentOpenOptions(agent.provider, options)
  const workingDir = options.workingDir ?? newWorkingDir(agent)
  options.prepare?.(workingDir)
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, request)
  return { agentId, workingDir }
}

/** The fixtures that an authenticated agent workspace fixture reads. */
interface AuthenticatedWorkspaceFixtures {
  page: Page
  leapmuxServer: WorkspaceServer & { agentEnv: Record<string, string> }
}

/** What an authenticated agent workspace fixture states. */
export interface AuthenticatedAgentWorkspaceOptions extends ProviderAgent {
  openOptions?: AgentOpenOverrides
  /**
   * Attach the native diagnostics of the provider after a failed test, while the agent still runs.
   * A failed diagnostic never replaces the failure of the test: the fixture attaches its error instead.
   */
  onFailure?: (testInfo: TestInfo, server: AuthenticatedWorkspaceFixtures['leapmuxServer']) => Promise<void>
}

/**
 * A Playwright fixture that opens one agent of a provider, signs in, and shows its workspace.
 * Each provider fixture file states its workspace fixtures through this factory.
 */
export function authenticatedAgentWorkspace(options: AuthenticatedAgentWorkspaceOptions) {
  return async ({ page, leapmuxServer }: AuthenticatedWorkspaceFixtures, use: (workspace: AgentWorkspace) => Promise<void>, testInfo: TestInfo): Promise<void> => {
    await withAgentWorkspace(leapmuxServer, options, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      try {
        await use(workspace)
      }
      finally {
        if (options.onFailure && testInfo.status !== testInfo.expectedStatus)
          await attachFailureDiagnostics(options.onFailure, testInfo, leapmuxServer)
      }
    })
  }
}

/**
 * Run a diagnostic of a failed test. Its own failure becomes an attachment, so it cannot replace the test failure.
 * An attachment that fails as well reaches the report beside the test failure.
 */
async function attachFailureDiagnostics(
  onFailure: NonNullable<AuthenticatedAgentWorkspaceOptions['onFailure']>,
  testInfo: TestInfo,
  server: AuthenticatedWorkspaceFixtures['leapmuxServer'],
): Promise<void> {
  try {
    await onFailure(testInfo, server)
  }
  catch (error) {
    await testInfo.attach('native-diagnostics-error', {
      body: error instanceof Error ? error.stack ?? error.message : String(error),
      contentType: 'text/plain',
    })
  }
}
