/**
 * Letta Code E2E fixtures.
 *
 * The worker starts one `letta server --listen ws://127.0.0.1:0` App Server per
 * agent and drives it over the protocol_v2 WebSocket. The default model uses
 * `openai-compatible`. The image test uses a built-in OpenAI vision model.
 * Both provider records point at the isolated model server.
 * Tests use no Letta Cloud account or real model. They never read the developer's `~/.letta`.
 *
 * Letta controls when its session-title and child turns run.
 * A spec that spawns a subagent must script the child's turn as a `rule` because its order is unknown.
 * Every test answers the title turn through `LETTA_TITLE_RULE`, which matches its prompt.
 *
 * The skip check finds the `letta` binary without running it.
 * Subagents execute `letta` from PATH. A mise shim fails under the isolated HOME.
 * Thus `helpers/mockAgentEnvironment.ts` puts the actual installation directory first on PATH.
 * See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import { LETTA_MODE } from '../../src/generated/contracts/letta-protocol'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { LETTA_REASONING_MODEL_ID, LETTA_VISION_MODEL_ID } from './helpers/mockAgentEnvironment'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { LETTA_TITLE_RULE } from './letta-code/housekeeping'
import { LETTA_AGENT, nativeContext } from './letta-code/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const LETTA_E2E_SKIP_REASON: string | null = missingBinaryReason('letta', 'Letta Code E2E requires the letta CLI on PATH (https://docs.letta.com/letta-code)')

/**
 * The agent opens in Unrestricted, which answers every tool call at once.
 *
 * LeapMux opens a new Letta session in Standard, which raises a banner for a
 * tool that requires approval. A spec that tests tool rendering would otherwise answer a banner before each call.
 * The control-request spec opens its own workspace in Standard.
 */
const UNRESTRICTED = { optionValues: { permissionMode: LETTA_MODE.Unrestricted } }
const VISION_UNRESTRICTED = { model: LETTA_VISION_MODEL_ID, optionValues: UNRESTRICTED.optionValues }
const REASONING_UNRESTRICTED = { model: LETTA_REASONING_MODEL_ID, optionValues: UNRESTRICTED.optionValues }

export const lettaTest = createProviderTest({
  agent: LETTA_AGENT,
  nativeContext,
  skipReason: LETTA_E2E_SKIP_REASON,
  /** An agent in Unrestricted, which raises no banner for a tool call. */
  workspaceName: 'authenticatedLettaWorkspace',
  workspaceOptions: { openOptions: UNRESTRICTED },
  // Letta runs its session-title turn at a time that no test controls, so every test of the provider answers it.
  // A spec that needs another answer registers a rule with another name. A newer rule matches first.
  modelRules: [LETTA_TITLE_RULE],
}).extend<{
  /** A built-in vision model routed through a local provider record. */
  authenticatedVisionLettaWorkspace: AgentWorkspace
  /** A built-in reasoning model routed through a local provider record. */
  authenticatedReasoningLettaWorkspace: AgentWorkspace
  /** An agent in LeapMux's default Standard mode, which asks before an approval tool. */
  askingLettaWorkspace: AgentWorkspace
}>({
  authenticatedVisionLettaWorkspace: authenticatedAgentWorkspace({ ...LETTA_AGENT, prefix: 'letta-e2e-vision', openOptions: VISION_UNRESTRICTED }),
  authenticatedReasoningLettaWorkspace: authenticatedAgentWorkspace({ ...LETTA_AGENT, prefix: 'letta-e2e-reasoning', openOptions: REASONING_UNRESTRICTED }),
  askingLettaWorkspace: authenticatedAgentWorkspace({ ...LETTA_AGENT, prefix: 'letta-e2e-ask' }),
})
