/**
 * Factory Droid E2E fixtures.
 *
 * The worker drives one `droid exec --input-format stream-jsonrpc` process per
 * agent. Droid calls the model through a custom-model entry with a supplied API key.
 * `helpers/mockAgentEnvironment.ts` writes that entry into the isolated `FACTORY_HOME_OVERRIDE` settings and points it at the mock endpoint.
 * Tests use no Factory account or real model. They never read the developer's `~/.factory`.
 *
 * The mock must answer Droid's session-title housekeeping turn before the first real turn.
 * Otherwise, that turn consumes a scripted step.
 * Every test answers it through `DROID_TITLE_RULE`, which matches the title helper's prompt and never depends on call order.
 *
 * The skip check finds the `droid` binary without running it.
 * The check uses the developer's HOME. Droid writes into `~/.factory` on every start.
 * See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { DROID_EFFORT, DROID_MODE } from '../../src/generated/contracts/droid-protocol'
import { DROID_TITLE_RULE } from './factory-droid/housekeeping'
import { DROID_AGENT, nativeContext } from './factory-droid/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'

export const DROID_E2E_SKIP_REASON: string | null = missingBinaryReason('droid', 'Factory Droid E2E requires the droid CLI on PATH (https://docs.factory.ai/)')

/**
 * The agent opens in Auto (High), which answers every tool call at once.
 *
 * LeapMux opens a new Droid session in Default, which asks before a tool that
 * changes something. A spec that tests tool rendering would otherwise answer
 * a banner before each call. The control-request spec opens its own workspace in
 * Default.
 */
const AUTO_HIGH = { optionValues: { permissionMode: DROID_MODE.AutoHigh } }
const REASONING_AUTO_HIGH = { optionValues: { ...AUTO_HIGH.optionValues, [OPTION_ID_EFFORT]: DROID_EFFORT.High } }

export const droidTest = createProviderTest({
  agent: DROID_AGENT,
  nativeContext,
  skipReason: DROID_E2E_SKIP_REASON,
  /** An agent in Auto (High), which raises no banner for a tool call. */
  workspaceName: 'authenticatedDroidWorkspace',
  workspaceOptions: { openOptions: AUTO_HIGH },
  // Droid runs its session-title turn at a time that no test controls, so every test of the provider answers it.
  // A spec that needs another answer registers a rule with another name. A newer rule matches first.
  modelRules: [DROID_TITLE_RULE],
}).extend<{
  /** A custom-model agent with high reasoning effort. */
  authenticatedReasoningDroidWorkspace: AgentWorkspace
  /** An agent in LeapMux's default Default mode, which asks before a change. */
  askingDroidWorkspace: AgentWorkspace
}>({
  authenticatedReasoningDroidWorkspace: authenticatedAgentWorkspace({ ...DROID_AGENT, prefix: 'droid-e2e-reasoning', openOptions: REASONING_AUTO_HIGH }),
  askingDroidWorkspace: authenticatedAgentWorkspace({ ...DROID_AGENT, prefix: 'droid-e2e-ask' }),
})
