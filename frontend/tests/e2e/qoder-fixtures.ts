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
import type { TestInfo } from '@playwright/test'
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { existsSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { QODER_MODE } from '../../src/generated/contracts/qoder-protocol'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'
import { nativeContext, QODER_AGENT } from './qoder-cli/scenarios'

export const QODER_E2E_SKIP_REASON: string | null = missingBinaryReason('qodercli', 'Qoder E2E requires the qodercli CLI on PATH (https://qoder.com)')

/**
 * The agent opens in Accept Edits, which answers every edit at once.
 *
 * The control-request spec opens its own workspace in Default.
 */
const ACCEPT_EDITS = { optionValues: { permissionMode: QODER_MODE.AcceptEdits } }

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

const QODER_DIAGNOSTICS = { onFailure: (testInfo: TestInfo, server: { agentEnv: Record<string, string> }) => attachQoderNativeLog(server.agentEnv, testInfo) }

export const qoderTest = base.extend<CliSkipFixture & NativeFixture & {
  /** An agent in Accept Edits, which answers every edit at once. */
  authenticatedQoderWorkspace: AgentWorkspace
  /**
   * An agent in Default mode, which raises a banner for each tool call. The
   * control-request spec needs the banner; the accept-edits workspace answers
   * every edit at once and would never raise one.
   */
  askingQoderWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(QODER_E2E_SKIP_REASON),
  authenticatedQoderWorkspace: authenticatedAgentWorkspace({ ...QODER_AGENT, openOptions: ACCEPT_EDITS, ...QODER_DIAGNOSTICS }),
  askingQoderWorkspace: authenticatedAgentWorkspace({ ...QODER_AGENT, prefix: 'qoder-e2e-ask', ...QODER_DIAGNOSTICS }),
  native: async ({ page, modelScript, leapmuxServer, authenticatedQoderWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId }))
  },
})

export { expect }
