/**
 * Qoder CLI E2E fixtures.
 *
 * The worker runs `qodercli --config-dir <dir> -p --input-format stream-json
 * --output-format stream-json` and drives it over NDJSON. Qoder's auth wall
 * blocks stream-json without login, so the E2E recipe mocks authentication the
 * way Cursor and Copilot do: `helpers/mockAgentEnvironment.ts` serves auth and
 * model from the mock endpoint under an isolated `--config-dir`. No test reaches
 * a Qoder account, a real model, or the developer's own Qoder configuration.
 */
import type { Page } from '@playwright/test'
import { QODER_MODE } from '../../src/generated/contracts/qoder-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { refreshQoderSdkAuthPayload } from './helpers/mockAgentEnvironment'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

export const QODER_E2E_SKIP_REASON: string | null = missingBinaryReason('qodercli', 'Qoder E2E requires the qodercli CLI on PATH (https://qoder.com)')

/** A working directory that is the root of a git repository of its own. */
export function createQoderWorkingDir(): string {
  return createGitRepo(createTestDirectory('qoder-e2e-wd-'), 'repo')
}

/** One Qoder agent's workspace, and the directory the agent works in. */
export interface QoderWorkspaceFixture {
  workspaceId: string
  workingDir: string
}

interface QoderAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
  agentEnv: Record<string, string>
}

/**
 * The agent opens in Accept Edits, which answers every edit at once.
 *
 * The control-request spec opens its own workspace in Default.
 */
const ACCEPT_EDITS = { optionValues: { permissionMode: QODER_MODE.AcceptEdits } }

function qoderWorkspace(prefix: string, openOptions?: { optionValues: Record<string, string> }) {
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: QoderAgentServer }, use: (fixture: QoderWorkspaceFixture) => Promise<void>) => {
    // The CLI consumes the SDK auth payload on first read; every launch needs
    // its own copy of the one-shot credential file.
    refreshQoderSdkAuthPayload(leapmuxServer.agentEnv)
    const workingDir = createQoderWorkingDir()
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.QODER, prefix, ...(openOptions ? { openOptions } : {}), workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  }
}

export const qoderTest = base.extend<{ qoderWorkspace: QoderWorkspaceFixture, askingQoderWorkspace: QoderWorkspaceFixture }>({
  qoderWorkspace: qoderWorkspace('qoder-e2e', ACCEPT_EDITS),
  // An agent in Default mode, which raises a banner for each tool call. The
  // control-request spec needs the banner; the accept-edits workspace answers
  // every edit at once and would never raise one.
  askingQoderWorkspace: qoderWorkspace('qoder-e2e-ask'),
})

/**
 * Open a Qoder agent in a directory the test knows, with the pinned model and
 * the option values the test states over it.
 */
export async function openQoderAgent(
  server: QoderAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
  workingDir: string = createQoderWorkingDir(),
): Promise<{ agentId: string, workingDir: string }> {
  refreshQoderSdkAuthPayload(server.agentEnv)
  const settings = agentOpenOptions(agentSettings(AgentProvider.QODER))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.QODER,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}

export { expect }
