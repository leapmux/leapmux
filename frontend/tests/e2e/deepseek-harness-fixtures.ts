import type { AgentWorkspace } from './helpers/workspace'
import { DEEPSEEK_HARNESS_MODE, DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '../../src/generated/contracts/deepseek-harness-protocol'
import { DEEPSEEK_HARNESS_AGENT, nativeContext } from './deepseek-harness/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'

export const DEEPSEEK_HARNESS_E2E_SKIP_REASON: string | null = missingBinaryReason('dsh', 'DeepSeek Harness E2E requires the native dsh executable.')

/** The option values of an Act agent with one permission preset. */
function actWith(permissions: string) {
  return {
    optionValues: { permissionMode: DEEPSEEK_HARNESS_MODE.Act, [DEEPSEEK_HARNESS_OPTION.Permissions]: permissions },
  }
}

export const deepseekHarnessTest = createProviderTest({
  agent: DEEPSEEK_HARNESS_AGENT,
  nativeContext,
  skipReason: DEEPSEEK_HARNESS_E2E_SKIP_REASON,
  /** An Act agent with full access, which runs every tool call at once. */
  workspaceName: 'authenticatedDeepseekHarnessWorkspace',
  workspaceOptions: {
    openOptions: actWith(DEEPSEEK_HARNESS_PERMISSION_PRESET.DangerFullAccess),
  },
}).extend<{
  /** An Act agent with workspace write access, which asks before a command escalates out of its sandbox. */
  askingDeepseekHarnessWorkspace: AgentWorkspace
}>({
  askingDeepseekHarnessWorkspace: authenticatedAgentWorkspace({
    ...DEEPSEEK_HARNESS_AGENT,
    prefix: 'deepseek-harness-e2e-ask',
    openOptions: actWith(DEEPSEEK_HARNESS_PERMISSION_PRESET.WorkspaceWrite),
  }),
})
