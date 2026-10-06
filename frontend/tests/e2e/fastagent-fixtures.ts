/**
 * Fast Agent e2e test fixtures.
 *
 * fast-agent fixes its model at session creation: `set_config_option` raises
 * `method_not_found`, so there is no per-session model switch. A spec that needs
 * another model opens its own agent with a `model` override.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const FAST_AGENT_E2E_SKIP_REASON: string | null = missingBinaryReason('fast-agent', 'Fast Agent E2E requires a fast-agent CLI on PATH')

/** How a Fast Agent agent opens. */
export const FAST_AGENT_AGENT: ProviderAgent = { provider: AgentProvider.FAST_AGENT, prefix: 'fastagent-e2e' }

export const fastAgentTest = base.extend<CliSkipFixture & {
  authenticatedFastAgentWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(FAST_AGENT_E2E_SKIP_REASON),
  authenticatedFastAgentWorkspace: authenticatedAgentWorkspace(FAST_AGENT_AGENT),
})

export { expect }
