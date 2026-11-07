/**
 * Fast Agent e2e test fixtures.
 *
 * fast-agent fixes its model at session creation: `set_config_option` raises
 * `method_not_found`, so there is no per-session model switch. A spec that needs
 * another model opens its own agent with a `model` override.
 */
import { FAST_AGENT_AGENT, nativeContext } from './fast-agent/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const fastAgentTest = createProviderTest({
  agent: FAST_AGENT_AGENT,
  nativeContext,
  workspaceName: 'authenticatedFastAgentWorkspace',
})
