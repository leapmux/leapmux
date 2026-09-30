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
import type { Page, TestInfo } from '@playwright/test'
import { existsSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { QODER_MODE } from '../../src/generated/contracts/qoder-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { missingBinaryReason } from './helpers/binaryOnPath'
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
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: QoderAgentServer }, use: (fixture: QoderWorkspaceFixture) => Promise<void>, testInfo: TestInfo) => {
    const workingDir = createQoderWorkingDir()
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.QODER, prefix, ...(openOptions ? { openOptions } : {}), workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      try {
        await use({ ...workspace, workingDir })
      }
      finally {
        if (testInfo.status !== testInfo.expectedStatus)
          await attachQoderNativeLog(leapmuxServer.agentEnv, testInfo)
      }
    })
  }
}

/** Keep the native endpoint trace when a Qoder browser test fails. */
async function attachQoderNativeLog(agentEnv: Record<string, string>, testInfo: TestInfo): Promise<void> {
  const authFile = agentEnv.QODER_SDK_AUTH_PAYLOAD_FILE
  if (!authFile)
    return
  const runsDir = join(dirname(authFile), 'logs', 'runs')
  if (!existsSync(runsDir))
    return
  const runs = readdirSync(runsDir, { withFileTypes: true })
  const latest = runs.filter(entry => entry.isDirectory()).map(entry => entry.name).sort().at(-1)
  if (!latest)
    return
  const path = join(runsDir, latest, 'qodercli.log')
  if (existsSync(path))
    await testInfo.attach('qoder-native-log', { path, contentType: 'text/plain' })
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
  const settings = agentOpenOptions(agentSettings(AgentProvider.QODER))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.QODER,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}

export { expect }

/** Assert the Qoder mode chip, without matching the separate effort chip. */
export async function expectQoderModeChip(page: Page, mode: string): Promise<void> {
  await expect(page.locator('[data-testid="composer-mode-trigger"]:visible')).toContainText(mode)
}
