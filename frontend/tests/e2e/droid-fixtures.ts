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
 * rule keys off the title helper's own system prompt, never off call order.
 *
 * The skip check looks for the `droid` binary without running it: the check runs
 * with the developer's own HOME, and Droid writes into `~/.factory` on every
 * start. See `helpers/binaryOnPath.ts`.
 */
import type { Page } from '@playwright/test'
import type { MockModelRule } from './helpers/mockModelScript'
import type { WorkspaceFixture } from './helpers/workspace'
import { DROID_MODE } from '../../src/generated/contracts/droid-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export const DROID_E2E_SKIP_REASON: string | null = missingBinaryReason('droid', 'Factory Droid E2E requires the droid CLI on PATH (https://docs.factory.ai/)')

/**
 * Droid's session-title housekeeping turn.
 *
 * The CLI names a session through a helper whose system prompt opens with this
 * sentence. A mock that keyed the answer off call order would let this turn eat
 * the step the test scripted for the real one. The prompt is the anchor; the
 * research report captured it from a live mock request.
 */
const DROID_TITLE_PROMPT_FRAGMENT = 'session titles for a session picker'

/**
 * The rule that answers the title turn, so no scripted step is consumed by it.
 *
 * Matched on the request BODY rather than the system slot: the title helper's
 * prompt is the anchor whatever message slot carries it, and a real turn never
 * contains that sentence.
 */
export const DROID_TITLE_RULE: MockModelRule = {
  name: 'title-droid',
  when: { body: DROID_TITLE_PROMPT_FRAGMENT },
  respond: { text: 'LeapMux E2E' },
}

/** One Factory Droid agent's workspace, and the directory the agent works in. */
export interface DroidWorkspaceFixture extends WorkspaceFixture {
  workingDir: string
}

/**
 * The agent opens in Auto (High), which answers every tool call at once.
 *
 * LeapMux opens a new Droid session in Default, which asks before a tool that
 * changes something. A spec that tests what a tool DRAWS would otherwise answer
 * a banner before each call. The control-request spec opens its own workspace in
 * Default.
 */
const AUTO_HIGH = { optionValues: { permissionMode: DROID_MODE.AutoHigh } }

interface DroidAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/** Open one agent in a fresh directory, log in, and show its workspace. */
function droidWorkspace(prefix: string, openOptions?: { optionValues: Record<string, string> }) {
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: DroidAgentServer }, use: (fixture: DroidWorkspaceFixture) => Promise<void>) => {
    const workingDir = createTestDirectory('droid-e2e-wd-')
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.DROID, prefix, ...(openOptions ? { openOptions } : {}), workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  }
}

export const droidTest = base.extend<{
  /** An agent in Auto (High), which raises no banner for a tool call. */
  authenticatedDroidWorkspace: DroidWorkspaceFixture
  /** An agent in LeapMux's default Default mode, which asks before a change. */
  askingDroidWorkspace: DroidWorkspaceFixture
}>({
  authenticatedDroidWorkspace: droidWorkspace('droid-e2e', AUTO_HIGH),
  askingDroidWorkspace: droidWorkspace('droid-e2e-ask'),
})

export { expect }

/** The model request that a recorded call carries. */
export function modelOf(body: unknown): string | undefined {
  return (body as { model?: string } | undefined)?.model
}
