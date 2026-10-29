/**
 * Copilot-specific e2e test fixtures.
 */
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { COPILOT_PERMISSION_MODE } from '../../src/generated/contracts/copilot-protocol'
import { attachCopilotNativeLogs } from './github-copilot/nativeLogs'
import { COPILOT_AGENT, nativeContext } from './github-copilot/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

export const COPILOT_E2E_SKIP_REASON: string | null = missingBinaryReason('copilot', 'Copilot E2E requires a copilot CLI on PATH')

export const copilotTest = createProviderTest({
  agent: COPILOT_AGENT,
  nativeContext,
  skipReason: COPILOT_E2E_SKIP_REASON,
  workspaceName: 'authenticatedCopilotWorkspace',
  workspaceOptions: {
    openOptions: {
      optionValues: { [OPTION_ID_PERMISSION_MODE]: COPILOT_PERMISSION_MODE.Manual },
    },
    // Every failed Copilot test keeps the native event and process logs of the isolated runtime.
    onFailure: (testInfo, server) => attachCopilotNativeLogs(server.agentEnv.COPILOT_HOME, testInfo),
  },
})
