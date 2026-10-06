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
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { expect } from '@playwright/test'
import { CODEBUDDY_MODE } from '../../src/generated/contracts/codebuddy-protocol'
import { CODEBUDDY_AGENT, nativeContext } from './codebuddy-code/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const CODEBUDDY_E2E_SKIP_REASON: string | null = missingBinaryReason('codebuddy', 'CodeBuddy E2E requires the codebuddy CLI on PATH (https://cnb.cool/codebuddy/codebuddy-code)')

/**
 * The agent opens in Bypass Permissions, which answers every tool call at once.
 *
 * The control-request spec opens its own workspace in Default.
 */
export const CODEBUDDY_BYPASS = { optionValues: { permissionMode: CODEBUDDY_MODE.BypassPermissions } }

export const codebuddyTest = base.extend<CliSkipFixture & NativeFixture & {
  /** An agent in Bypass Permissions, which raises no banner for a tool call. */
  authenticatedCodebuddyWorkspace: AgentWorkspace
  /**
   * An agent in Default mode, which raises a banner for each tool call. The
   * control-request spec needs the banner; the bypass workspace answers every
   * call at once and would never raise one.
   */
  askingCodebuddyWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CODEBUDDY_E2E_SKIP_REASON),
  authenticatedCodebuddyWorkspace: authenticatedAgentWorkspace({ ...CODEBUDDY_AGENT, openOptions: CODEBUDDY_BYPASS }),
  askingCodebuddyWorkspace: authenticatedAgentWorkspace({ ...CODEBUDDY_AGENT, prefix: 'codebuddy-e2e-ask' }),
  native: async ({ page, modelScript, leapmuxServer, authenticatedCodebuddyWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId }))
  },
})

export { expect }
