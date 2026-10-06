/**
 * Junie e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { expect } from '@playwright/test'
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { JUNIE_NATIVE_EFFORT_MODEL, JUNIE_RESPONSES_MODEL } from './helpers/mockAgentEnvironment'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { JUNIE_HOUSEKEEPING_RULES } from './junie/housekeeping'
import { JUNIE_AGENT, nativeContext } from './junie/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const JUNIE_E2E_SKIP_REASON: string | null = missingBinaryReason('junie', 'Junie E2E requires a junie CLI on PATH')

export const junieTest = base.extend<CliSkipFixture & NativeFixture & {
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
  // Junie runs its capability-filter and task-name turns at times that no test controls, so every test of the
  // provider answers them. A spec that needs another answer registers its own rule under another name: a newer rule
  // matches first.
  modelScript: async ({ modelScript }, use) => {
    await modelScript.rule(...JUNIE_HOUSEKEEPING_RULES)
    await use(modelScript)
  },
  native: async ({ page, modelScript, leapmuxServer, authenticatedJunieWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId }))
  },
})

export { expect }
