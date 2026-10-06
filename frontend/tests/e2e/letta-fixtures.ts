/**
 * Letta Code E2E fixtures.
 *
 * The worker starts one `letta server --listen ws://127.0.0.1:0` App Server per
 * agent and drives it over the protocol_v2 WebSocket. The default model uses
 * `openai-compatible`. The image test uses a built-in OpenAI vision model.
 * Both provider records point at the isolated model server. No test
 * reaches a Letta Cloud account, a real model, or the developer's own `~/.letta`.
 *
 * Letta runs its session title and subagent child turns by itself. A spec that
 * spawns a subagent must script the child's turn as a `rule` (it cannot be placed
 * in order), and any spec must tolerate a title turn by keying it off the prompt.
 *
 * The skip check looks for the `letta` binary without running it. Subagents
 * re-exec `letta` from PATH, and a mise shim fails under the isolated HOME, so
 * `helpers/mockAgentEnvironment.ts` puts the real install directory first on
 * PATH. See `helpers/binaryOnPath.ts`.
 */
import type { MockModelRule } from './helpers/mockModelScript'
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { LETTA_MODE } from '../../src/generated/contracts/letta-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { LETTA_REASONING_MODEL_ID, LETTA_VISION_MODEL_ID } from './helpers/mockAgentEnvironment'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const LETTA_E2E_SKIP_REASON: string | null = missingBinaryReason('letta', 'Letta Code E2E requires the letta CLI on PATH (https://docs.letta.com/letta-code)')

/**
 * Letta's session-title housekeeping turn, keyed off the request body so it
 * cannot consume a scripted step whatever slot the prompt sits in.
 */
export const LETTA_TITLE_RULE: MockModelRule = {
  name: 'title-letta',
  when: { body: 'session title' },
  respond: { text: 'LeapMux E2E' },
}

/** How a Letta Code agent opens. */
export const LETTA_AGENT: ProviderAgent = { provider: AgentProvider.LETTA, prefix: 'letta-e2e' }

/**
 * The agent opens in Unrestricted, which answers every tool call at once.
 *
 * LeapMux opens a new Letta session in Standard, which raises a banner for a
 * tool the runtime marks as needing approval. A spec that tests what a tool
 * DRAWS would otherwise answer a banner before each call. The control-request
 * spec opens its own workspace in Standard.
 */
const UNRESTRICTED = { optionValues: { permissionMode: LETTA_MODE.Unrestricted } }
const VISION_UNRESTRICTED = { model: LETTA_VISION_MODEL_ID, optionValues: UNRESTRICTED.optionValues }
const REASONING_UNRESTRICTED = { model: LETTA_REASONING_MODEL_ID, optionValues: UNRESTRICTED.optionValues }

export const lettaTest = base.extend<CliSkipFixture & {
  /** An agent in Unrestricted, which raises no banner for a tool call. */
  authenticatedLettaWorkspace: AgentWorkspace
  /** A built-in vision model routed through a local provider record. */
  authenticatedVisionLettaWorkspace: AgentWorkspace
  /** A built-in reasoning model routed through a local provider record. */
  authenticatedReasoningLettaWorkspace: AgentWorkspace
  /** An agent in LeapMux's default Standard mode, which asks before an approval tool. */
  askingLettaWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(LETTA_E2E_SKIP_REASON),
  authenticatedLettaWorkspace: authenticatedAgentWorkspace({ ...LETTA_AGENT, openOptions: UNRESTRICTED }),
  authenticatedVisionLettaWorkspace: authenticatedAgentWorkspace({ ...LETTA_AGENT, prefix: 'letta-e2e-vision', openOptions: VISION_UNRESTRICTED }),
  authenticatedReasoningLettaWorkspace: authenticatedAgentWorkspace({ ...LETTA_AGENT, prefix: 'letta-e2e-reasoning', openOptions: REASONING_UNRESTRICTED }),
  askingLettaWorkspace: authenticatedAgentWorkspace({ ...LETTA_AGENT, prefix: 'letta-e2e-ask' }),
})

export { expect }
