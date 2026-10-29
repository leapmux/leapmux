/**
 * Qoder CLI E2E fixtures.
 *
 * The worker runs `qodercli --config-dir <dir> -p --input-format stream-json
 * --output-format stream-json` and drives it over newline-delimited JSON (NDJSON).
 * Qoder requires login for stream-json. These tests mock authentication as the Cursor and Copilot tests do.
 * `helpers/mockAgentEnvironment.ts` configures authentication and model calls through the mock endpoint under an isolated `--config-dir`.
 * Tests use no Qoder account or real model. They never read the developer's Qoder configuration.
 */
import type { TestInfo } from '@playwright/test'
import type { AgentWorkspace } from './helpers/workspace'
import { existsSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { QODER_MODE } from '../../src/generated/contracts/qoder-protocol'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'
import { nativeContext, QODER_AGENT } from './qoder-cli/scenarios'

export const QODER_E2E_SKIP_REASON: string | null = missingBinaryReason('qodercli', 'Qoder E2E requires the qodercli CLI on PATH (https://qoder.com)')

/**
 * The agent opens in Accept Edits, which answers every edit at once.
 *
 * The control-request spec opens its own workspace in Default.
 */
const ACCEPT_EDITS = {
  optionValues: { permissionMode: QODER_MODE.AcceptEdits },
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

const QODER_DIAGNOSTICS = {
  onFailure: (testInfo: TestInfo, server: { agentEnv: Record<string, string> }) => attachQoderNativeLog(server.agentEnv, testInfo),
}

export const qoderTest = createProviderTest({
  agent: QODER_AGENT,
  nativeContext,
  skipReason: QODER_E2E_SKIP_REASON,
  /** An agent in Accept Edits, which answers every edit at once. */
  workspaceName: 'authenticatedQoderWorkspace',
  workspaceOptions: { openOptions: ACCEPT_EDITS, ...QODER_DIAGNOSTICS },
}).extend<{
  /**
   * An agent in Default mode, which raises a banner for each tool call.
   * The control-request spec needs the banner.
   * The accept-edits workspace answers every edit at once and never raises a banner for an edit.
   */
  askingQoderWorkspace: AgentWorkspace
}>({
  askingQoderWorkspace: authenticatedAgentWorkspace({
    ...QODER_AGENT,
    prefix: 'qoder-e2e-ask',
    ...QODER_DIAGNOSTICS,
  }),
})
