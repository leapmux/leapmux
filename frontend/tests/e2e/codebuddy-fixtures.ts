/**
 * CodeBuddy Code E2E fixtures.
 *
 * The worker runs `codebuddy -p --input-format stream-json --output-format
 * stream-json` and drives it over NDJSON. CodeBuddy calls the model through a
 * custom-local `models.json` entry, which `helpers/mockAgentEnvironment.ts`
 * points at the mock endpoint under an isolated HOME and CODEBUDDY_CONFIG_DIR.
 * No test reaches a CodeBuddy account, a real model, or the developer's own
 * CodeBuddy configuration.
 *
 * The mock MUST stream SSE: CodeBuddy always sends `stream:true`, and a plain
 * JSON completion is dropped with `error_during_execution`.
 */
import type { Page } from '@playwright/test'
import { CODEBUDDY_MODE } from '../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

export const CODEBUDDY_E2E_SKIP_REASON: string | null = missingBinaryReason('codebuddy', 'CodeBuddy E2E requires the codebuddy CLI on PATH (https://cnb.cool/codebuddy/codebuddy-code)')

/** A working directory that is the root of a git repository of its own. */
export function createCodebuddyWorkingDir(): string {
  return createGitRepo(createTestDirectory('codebuddy-e2e-wd-'), 'repo')
}

/** One CodeBuddy agent's workspace, and the directory the agent works in. */
export interface CodebuddyWorkspaceFixture {
  workspaceId: string
  workingDir: string
}

interface CodebuddyAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * The agent opens in Bypass Permissions, which answers every tool call at once.
 *
 * The control-request spec opens its own workspace in Default.
 */
const BYPASS = { optionValues: { permissionMode: CODEBUDDY_MODE.BypassPermissions } }

function codebuddyWorkspace(prefix: string, openOptions?: { optionValues: Record<string, string> }) {
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: CodebuddyAgentServer }, use: (fixture: CodebuddyWorkspaceFixture) => Promise<void>) => {
    const workingDir = createCodebuddyWorkingDir()
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.CODEBUDDY, prefix, ...(openOptions ? { openOptions } : {}), workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  }
}

export const codebuddyTest = base.extend<{ codebuddyWorkspace: CodebuddyWorkspaceFixture, askingCodebuddyWorkspace: CodebuddyWorkspaceFixture }>({
  codebuddyWorkspace: codebuddyWorkspace('codebuddy-e2e', BYPASS),
  // An agent in Default mode, which raises a banner for each tool call. The
  // control-request spec needs the banner; the bypass workspace answers every
  // call at once and would never raise one.
  askingCodebuddyWorkspace: codebuddyWorkspace('codebuddy-e2e-ask'),
})

/**
 * Open a CodeBuddy agent in a directory the test knows, with the pinned model and
 * the option values the test states over it.
 */
export async function openCodebuddyAgent(
  server: CodebuddyAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
  workingDir: string = createCodebuddyWorkingDir(),
): Promise<{ agentId: string, workingDir: string }> {
  const settings = agentOpenOptions(agentSettings(AgentProvider.CODEBUDDY))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.CODEBUDDY,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}

export { expect }
