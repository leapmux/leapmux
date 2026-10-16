/**
 * CodeBuddy Code E2E fixtures.
 *
 * The worker runs `codebuddy -p --input-format stream-json --output-format
 * stream-json` and drives it over newline-delimited JSON (NDJSON).
 * CodeBuddy calls the model through a custom-local `models.json` entry.
 * `helpers/mockAgentEnvironment.ts` points that entry at the mock endpoint under an isolated HOME and CODEBUDDY_CONFIG_DIR.
 * Tests use no CodeBuddy account or real model. They never read the developer's CodeBuddy configuration.
 *
 * The mock must stream server-sent events (SSE). CodeBuddy always sends `stream:true`.
 * CodeBuddy drops a plain JSON completion with `error_during_execution`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import { CODEBUDDY_MODE } from '../../src/generated/contracts/codebuddy-protocol'
import { CODEBUDDY_AGENT, nativeContext } from './codebuddy-code/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'

export const CODEBUDDY_E2E_SKIP_REASON: string | null = missingBinaryReason('codebuddy', 'CodeBuddy E2E requires the codebuddy CLI on PATH (https://cnb.cool/codebuddy/codebuddy-code)')

/**
 * The agent opens in Bypass Permissions, which answers every tool call at once.
 *
 * The control-request spec opens its own workspace in Default.
 */
export const CODEBUDDY_BYPASS = { optionValues: { permissionMode: CODEBUDDY_MODE.BypassPermissions } }

export const codebuddyTest = createProviderTest({
  agent: CODEBUDDY_AGENT,
  nativeContext,
  skipReason: CODEBUDDY_E2E_SKIP_REASON,
  /** An agent in Bypass Permissions, which raises no banner for a tool call. */
  workspaceName: 'authenticatedCodebuddyWorkspace',
  workspaceOptions: { openOptions: CODEBUDDY_BYPASS },
}).extend<{
  /**
   * An agent in Default mode, which raises a banner for each tool call.
   * The control-request spec needs the banner.
   * The bypass workspace answers every call at once and never raises a banner.
   */
  askingCodebuddyWorkspace: AgentWorkspace
}>({
  askingCodebuddyWorkspace: authenticatedAgentWorkspace({ ...CODEBUDDY_AGENT, prefix: 'codebuddy-e2e-ask' }),
})
