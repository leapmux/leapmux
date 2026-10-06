/**
 * Factory Droid E2E fixtures.
 *
 * The worker drives one `droid exec --input-format stream-jsonrpc` process per
 * agent. Droid calls the model through its BYOK custom-model entry, which
 * `helpers/mockAgentEnvironment.ts` writes into the isolated `FACTORY_HOME_OVERRIDE`
 * settings and points at the mock endpoint. No test reaches a Factory account, a
 * real model, or the developer's own `~/.factory`.
 *
 * The mock MUST answer Droid's session-title housekeeping turn, which fires
 * before the first real turn and would otherwise consume a scripted step. The
 * test object answers it for every test through `DROID_TITLE_RULE`, which keys
 * off the title helper's own prompt, never off call order.
 *
 * The skip check looks for the `droid` binary without running it: the check runs
 * with the developer's own HOME, and Droid writes into `~/.factory` on every
 * start. See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { OPTION_ID_EFFORT } from '../../src/components/chat/settingsGroups'
import { DROID_EFFORT, DROID_MODE } from '../../src/generated/contracts/droid-protocol'
import { DROID_TITLE_RULE } from './factory-droid/housekeeping'
import { DROID_AGENT, nativeContext } from './factory-droid/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const DROID_E2E_SKIP_REASON: string | null = missingBinaryReason('droid', 'Factory Droid E2E requires the droid CLI on PATH (https://docs.factory.ai/)')

/**
 * The agent opens in Auto (High), which answers every tool call at once.
 *
 * LeapMux opens a new Droid session in Default, which asks before a tool that
 * changes something. A spec that tests what a tool DRAWS would otherwise answer
 * a banner before each call. The control-request spec opens its own workspace in
 * Default.
 */
const AUTO_HIGH = { optionValues: { permissionMode: DROID_MODE.AutoHigh } }
const REASONING_AUTO_HIGH = { optionValues: { ...AUTO_HIGH.optionValues, [OPTION_ID_EFFORT]: DROID_EFFORT.High } }

export const droidTest = base.extend<CliSkipFixture & NativeFixture & {
  /** An agent in Auto (High), which raises no banner for a tool call. */
  authenticatedDroidWorkspace: AgentWorkspace
  /** A custom-model agent with high reasoning effort. */
  authenticatedReasoningDroidWorkspace: AgentWorkspace
  /** An agent in LeapMux's default Default mode, which asks before a change. */
  askingDroidWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(DROID_E2E_SKIP_REASON),
  authenticatedDroidWorkspace: authenticatedAgentWorkspace({ ...DROID_AGENT, openOptions: AUTO_HIGH }),
  authenticatedReasoningDroidWorkspace: authenticatedAgentWorkspace({ ...DROID_AGENT, prefix: 'droid-e2e-reasoning', openOptions: REASONING_AUTO_HIGH }),
  askingDroidWorkspace: authenticatedAgentWorkspace({ ...DROID_AGENT, prefix: 'droid-e2e-ask' }),
  // Droid runs its session-title turn at a time that no test controls, so every test of the provider answers it.
  // A spec that needs another answer registers its own rule under another name: a newer rule matches first.
  modelScript: async ({ modelScript }, use) => {
    await modelScript.rule(DROID_TITLE_RULE)
    await use(modelScript)
  },
  native: async ({ page, modelScript, leapmuxServer, authenticatedDroidWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId }))
  },
})
