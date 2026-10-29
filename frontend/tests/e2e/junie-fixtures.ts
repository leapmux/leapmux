/**
 * Junie e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { JUNIE_NATIVE_EFFORT_MODEL, JUNIE_RESPONSES_MODEL } from './helpers/mockAgentEnvironment'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { JUNIE_HOUSEKEEPING_RULES } from './junie/housekeeping'
import { JUNIE_AGENT, nativeContext } from './junie/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const JUNIE_E2E_SKIP_REASON: string | null = missingBinaryReason('junie', 'Junie E2E requires a junie CLI on PATH')

export const junieTest = createProviderTest({
  agent: JUNIE_AGENT,
  nativeContext,
  skipReason: JUNIE_E2E_SKIP_REASON,
  workspaceName: 'authenticatedJunieWorkspace',
  // Junie controls when its capability-filter and task-name turns run. Every test answers them with these rules.
  // A spec that needs another answer registers a rule with another name. A newer rule matches first.
  modelRules: JUNIE_HOUSEKEEPING_RULES,
}).extend<{
  authenticatedResponsesJunieWorkspace: AgentWorkspace
  authenticatedNativeEffortJunieWorkspace: AgentWorkspace
}>({
  authenticatedResponsesJunieWorkspace: authenticatedAgentWorkspace({
    ...JUNIE_AGENT,
    prefix: 'junie-e2e-responses',
    openOptions: { model: JUNIE_RESPONSES_MODEL },
  }),
  authenticatedNativeEffortJunieWorkspace: authenticatedAgentWorkspace({
    ...JUNIE_AGENT,
    prefix: 'junie-e2e-native-effort',
    openOptions: {
      model: JUNIE_NATIVE_EFFORT_MODEL,
      optionValues: { [OPTION_ID_EFFORT]: 'high' },
    },
  }),
})
