/**
 * Letta Code E2E fixtures.
 *
 * The worker starts one `letta server --listen ws://127.0.0.1:0` App Server per
 * agent and drives it over the protocol_v2 WebSocket. Letta calls the model
 * through its `openai-compatible` provider, which `helpers/mockAgentEnvironment.ts`
 * points at the mock endpoint in the isolated `LETTA_LOCAL_BACKEND_DIR`. No test
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
import type { Page } from '@playwright/test'
import type { MockModelRule } from './helpers/mockModelScript'
import type { WorkspaceFixture } from './helpers/workspace'
import { LETTA_MODE } from '../../src/generated/contracts/letta-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export const LETTA_E2E_SKIP_REASON: string | null = missingBinaryReason('letta', 'Letta Code E2E requires the letta CLI on PATH (https://docs.letta.com/letta-code)')

/**
 * A rule that answers a turn keyed off its system prompt, for a turn Letta
 * starts by itself.
 *
 * Letta's local backend names a conversation through a housekeeping turn. A mock
 * that keyed the answer off call order would let that turn eat the step the test
 * scripted for the real one. Pass a rule of your own when a spec needs a
 * different answer; the shared `title-system` housekeeping rule covers the usual
 * title prompt.
 */
export function lettaRule(name: string, body: string, respond: MockModelRule['respond']): MockModelRule {
  return { name, when: { body }, respond }
}

/**
 * Letta's session-title housekeeping turn, keyed off the request body so it
 * cannot consume a scripted step whatever slot the prompt sits in.
 */
export const LETTA_TITLE_RULE: MockModelRule = {
  name: 'title-letta',
  when: { body: 'session title' },
  respond: { text: 'LeapMux E2E' },
}

/** One Letta Code agent's workspace, and the directory the agent works in. */
export interface LettaWorkspaceFixture extends WorkspaceFixture {
  workingDir: string
}

/**
 * The agent opens in Unrestricted, which answers every tool call at once.
 *
 * LeapMux opens a new Letta session in Standard, which raises a banner for a
 * tool the runtime marks as needing approval. A spec that tests what a tool
 * DRAWS would otherwise answer a banner before each call. The control-request
 * spec opens its own workspace in Standard.
 */
const UNRESTRICTED = { optionValues: { permissionMode: LETTA_MODE.Unrestricted } }

interface LettaAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/** Open one agent in a fresh directory, log in, and show its workspace. */
function lettaWorkspace(prefix: string, openOptions?: { optionValues: Record<string, string> }) {
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: LettaAgentServer }, use: (fixture: LettaWorkspaceFixture) => Promise<void>) => {
    const workingDir = createTestDirectory('letta-e2e-wd-')
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.LETTA, prefix, ...(openOptions ? { openOptions } : {}), workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  }
}

export const lettaTest = base.extend<{
  /** An agent in Unrestricted, which raises no banner for a tool call. */
  authenticatedLettaWorkspace: LettaWorkspaceFixture
  /** An agent in LeapMux's default Standard mode, which asks before an approval tool. */
  askingLettaWorkspace: LettaWorkspaceFixture
}>({
  authenticatedLettaWorkspace: lettaWorkspace('letta-e2e', UNRESTRICTED),
  askingLettaWorkspace: lettaWorkspace('letta-e2e-ask'),
})

export { expect }
