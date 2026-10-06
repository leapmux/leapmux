/**
 * Fast Agent e2e test fixtures.
 *
 * fast-agent fixes its model at session creation: `set_config_option` raises
 * `method_not_found`, so there is no per-session model switch. A spec that needs
 * another model opens its own agent with a `model` override.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { expect } from '@playwright/test'
import { FAST_AGENT_AGENT, nativeContext } from './fast-agent/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const FAST_AGENT_E2E_SKIP_REASON: string | null = missingBinaryReason('fast-agent', 'Fast Agent E2E requires a fast-agent CLI on PATH')

export const fastAgentTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedFastAgentWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(FAST_AGENT_E2E_SKIP_REASON),
  authenticatedFastAgentWorkspace: authenticatedAgentWorkspace(FAST_AGENT_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedFastAgentWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId }))
  },
})

export { expect }
