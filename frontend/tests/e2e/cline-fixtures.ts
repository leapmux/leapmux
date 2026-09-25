/**
 * Cline E2E fixtures.
 *
 * The worker starts one private Cline hub for each agent and drives it over Cline's
 * hub WebSocket protocol. Cline calls the model through its `openai-compatible`
 * provider, which `helpers/mockAgentEnvironment.ts` points at the mock endpoint in
 * Cline's own settings under an isolated HOME. No test reaches a Cline account, a
 * real model, or the developer's own Cline hub.
 *
 * The skip check looks for the `cline` binary without running it: the check runs
 * with the developer's own HOME, and Cline writes into its data directory on every
 * start. See `helpers/binaryOnPath.ts`.
 */
import type { Page } from '@playwright/test'
import type { WorkspaceFixture } from './helpers/workspace'
import { CLINE_PERMISSION_MODE } from '../../src/generated/contracts/cline-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

export const CLINE_E2E_SKIP_REASON: string | null = missingBinaryReason('cline', 'Cline E2E requires the cline CLI on PATH (https://cline.bot/cli)')

/**
 * A working directory that is the root of a git repository of its own.
 *
 * Cline reads rules, skills and workflows from the workspace it runs in, and it
 * takes the root of the git repository around the working directory as that
 * workspace. The run directory sits inside the LeapMux checkout, whose root holds an
 * `AGENTS.md`, and a repository of its own holds nothing that Cline reads.
 */
export function createClineWorkingDir(): string {
  return createGitRepo(createTestDirectory('cline-e2e-wd-'), 'repo')
}

/** One Cline agent's workspace, and the directory the agent works in. */
export interface ClineWorkspaceFixture extends WorkspaceFixture {
  workingDir: string
}

/**
 * The agent opens in Auto-approve, which answers every tool call at once.
 *
 * LeapMux opens a new Cline session in Act, which raises a banner for each edit and
 * each command. A spec that tests what a tool DRAWS would otherwise answer a banner
 * before each call. The control-request spec opens its own workspace in Act.
 */
const AUTO_APPROVE = { optionValues: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove } }

interface ClineAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/** Open one agent in a fresh repository, log in, and show its workspace. */
function clineWorkspace(prefix: string, openOptions?: { optionValues: Record<string, string> }) {
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: ClineAgentServer }, use: (fixture: ClineWorkspaceFixture) => Promise<void>) => {
    const workingDir = createClineWorkingDir()
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.CLINE, prefix, ...(openOptions ? { openOptions } : {}), workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  }
}

export const clineTest = base.extend<{
  /** An agent in Auto-approve, which raises no banner for a tool call. */
  authenticatedClineWorkspace: ClineWorkspaceFixture
  /** An agent in LeapMux's default Act mode, which asks before each edit and command. */
  askingClineWorkspace: ClineWorkspaceFixture
  /** An agent in Plan mode, which offers the plan tool. */
  planningClineWorkspace: ClineWorkspaceFixture
}>({
  authenticatedClineWorkspace: clineWorkspace('cline-e2e', AUTO_APPROVE),
  askingClineWorkspace: clineWorkspace('cline-e2e-act'),
  planningClineWorkspace: clineWorkspace('cline-e2e-plan', { optionValues: { permissionMode: CLINE_PERMISSION_MODE.Plan } }),
})

export { expect }

/**
 * The tool names that one recorded model call OFFERED, read from its `tools`.
 *
 * A tool name can also appear in the conversation that the call carries, as an
 * earlier call of the tool, so a search of the whole body cannot tell what the
 * session offers now.
 */
export function offeredTools(body: unknown): string[] {
  const tools = (body as { tools?: { function?: { name?: string } }[] } | undefined)?.tools ?? []
  return tools.map(tool => tool.function?.name ?? '')
}

/**
 * Open a Cline agent in a directory the test knows, with the pinned model and the
 * option values the test states over it.
 */
export async function openClineAgent(
  server: ClineAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
  workingDir: string = createClineWorkingDir(),
): Promise<{ agentId: string, workingDir: string }> {
  const settings = agentOpenOptions(agentSettings(AgentProvider.CLINE))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.CLINE,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
