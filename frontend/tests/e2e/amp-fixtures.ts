import type { AgentWorkspace } from './helpers/workspace'
import { AMP_PERMISSION_MODE } from '../../src/generated/contracts/amp-protocol'
import { AMP_AGENT, nativeContext } from './amp/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'

/**
 * Skip the Amp specs when the `amp` CLI is not installed.
 * The worker starts `amp --execute --stream-json`. Without the binary, the agent cannot take a turn.
 *
 * The check finds the binary without running it.
 * Playwright uses the developer's HOME. Amp reads its configuration and login there on every start.
 * See `helpers/binaryOnPath.ts`.
 */
export const AMP_E2E_SKIP_REASON: string | null = missingBinaryReason('amp', 'Amp E2E requires the amp CLI on PATH (https://ampcode.com)')

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
  skipReason: AMP_E2E_SKIP_REASON,
  workspaceName: 'authenticatedAmpWorkspace',
  workspaceOptions: { openOptions: AMP_ALLOW_ALL },
}).extend<{
  /** An agent in LeapMux's default Ask mode, which raises a banner for each call. */
  askingAmpWorkspace: AgentWorkspace
}>({
  askingAmpWorkspace: authenticatedAgentWorkspace({ ...AMP_AGENT, prefix: 'amp-e2e-ask' }),
})
