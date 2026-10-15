/**
 * Fast Agent e2e test fixtures.
 *
 * fast-agent fixes its model at session creation: `set_config_option` raises
 * `method_not_found`, so there is no per-session model switch. A spec that needs
 * another model opens its own agent with a `model` override.
 */
import { FAST_AGENT_AGENT, nativeContext } from './fast-agent/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

export const FAST_AGENT_E2E_SKIP_REASON: string | null = missingBinaryReason('fast-agent', 'Fast Agent E2E requires a fast-agent CLI on PATH')

export const fastAgentTest = createProviderTest({
  agent: FAST_AGENT_AGENT,
  nativeContext,
  skipReason: FAST_AGENT_E2E_SKIP_REASON,
  workspaceName: 'authenticatedFastAgentWorkspace',
})
