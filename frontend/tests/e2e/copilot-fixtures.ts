/**
 * Copilot-specific e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { COPILOT_PERMISSION_MODE } from '../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { nativeContext } from './github-copilot/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { attachCopilotNativeLogs } from './helpers/copilotNativeLogs'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const COPILOT_E2E_SKIP_REASON: string | null = missingBinaryReason('copilot', 'Copilot E2E requires a copilot CLI on PATH')

/** How a Copilot agent opens. */
export const COPILOT_AGENT: ProviderAgent = { provider: AgentProvider.GITHUB_COPILOT, prefix: 'copilot-e2e' }

export const copilotTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedCopilotWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(COPILOT_E2E_SKIP_REASON),
  authenticatedCopilotWorkspace: authenticatedAgentWorkspace({
    ...COPILOT_AGENT,
    openOptions: { optionValues: { [OPTION_ID_PERMISSION_MODE]: COPILOT_PERMISSION_MODE.Manual } },
    // Every failed Copilot test keeps the native event and process logs of the isolated runtime.
    onFailure: (testInfo, server) => attachCopilotNativeLogs(server.agentEnv.COPILOT_HOME, testInfo),
  }),
  native: async ({ page, modelScript, leapmuxServer, authenticatedCopilotWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId }))
  },
})

export { expect }
