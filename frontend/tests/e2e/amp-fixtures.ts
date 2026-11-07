import type { AgentWorkspace } from './helpers/workspace'
import { AMP_PERMISSION_MODE } from '../../src/generated/contracts/amp-protocol'
import { AMP_AGENT, nativeContext } from './amp/scenarios'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'

/**
 * The agent opens in Allow All, which answers every tool call at once.
 *
 * LeapMux opens a new Amp session in Ask, which raises a banner for every call that
 * Amp's executor runs. A spec that tests tool rendering would otherwise answer a
 * banner before each call. The permission spec opens its own workspace in Ask.
 */
export const AMP_ALLOW_ALL = {
  optionValues: { permissionMode: AMP_PERMISSION_MODE.AllowAll },
}

export const ampTest = createProviderTest({
  agent: AMP_AGENT,
  nativeContext,
  workspaceName: 'authenticatedAmpWorkspace',
  workspaceOptions: { openOptions: AMP_ALLOW_ALL },
}).extend<{
  /** An agent in LeapMux's default Ask mode, which raises a banner for each call. */
  askingAmpWorkspace: AgentWorkspace
}>({
  askingAmpWorkspace: authenticatedAgentWorkspace({ ...AMP_AGENT, prefix: 'amp-e2e-ask' }),
})
