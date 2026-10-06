/**
 * Junie e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { JUNIE_NATIVE_EFFORT_MODEL, JUNIE_RESPONSES_MODEL } from './helpers/mockAgentEnvironment'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const JUNIE_E2E_SKIP_REASON: string | null = missingBinaryReason('junie', 'Junie E2E requires a junie CLI on PATH')

/** How a Junie agent opens. */
export const JUNIE_AGENT: ProviderAgent = { provider: AgentProvider.JUNIE, prefix: 'junie-e2e' }

export const junieTest = base.extend<CliSkipFixture & {
  authenticatedJunieWorkspace: AgentWorkspace
  authenticatedResponsesJunieWorkspace: AgentWorkspace
  authenticatedNativeEffortJunieWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(JUNIE_E2E_SKIP_REASON),
  authenticatedJunieWorkspace: authenticatedAgentWorkspace(JUNIE_AGENT),
  authenticatedResponsesJunieWorkspace: authenticatedAgentWorkspace({
    ...JUNIE_AGENT,
    prefix: 'junie-e2e-responses',
    openOptions: { model: JUNIE_RESPONSES_MODEL },
  }),
  authenticatedNativeEffortJunieWorkspace: authenticatedAgentWorkspace({
    ...JUNIE_AGENT,
    prefix: 'junie-e2e-native-effort',
    openOptions: { model: JUNIE_NATIVE_EFFORT_MODEL, optionValues: { [OPTION_ID_EFFORT]: 'high' } },
  }),
})

export { expect }
