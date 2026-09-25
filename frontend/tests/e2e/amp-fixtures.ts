import { AMP_PERMISSION_MODE } from '../../src/generated/contracts/amp-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

/**
 * Skip the Amp specs when the `amp` CLI is not installed. The worker is what starts
 * `amp --execute --stream-json`. Without the binary the agent cannot take a turn.
 *
 * The check looks for the binary without running it: it runs in the Playwright
 * process, which carries the developer's own HOME, and Amp reads its configuration
 * and its login under that HOME on every start. See `helpers/binaryOnPath.ts`.
 */
export const AMP_E2E_SKIP_REASON: string | null = missingBinaryReason('amp', 'Amp E2E requires the amp CLI on PATH (https://ampcode.com)')

/** One Amp agent's workspace, and the directory the agent works in. */
export interface AmpWorkspaceFixture {
  workspaceId: string
  workingDir: string
}

/**
 * The agent opens in Allow All, which answers every tool call at once.
 *
 * LeapMux opens a new Amp session in Ask, which raises a banner for every call that
 * Amp's executor runs. A spec that tests what a tool DRAWS would otherwise answer a
 * banner before each call. The permission spec opens its own workspace in Ask.
 */
const ALLOW_ALL = { optionValues: { permissionMode: AMP_PERMISSION_MODE.AllowAll } }

export const ampTest = base.extend<{
  ampWorkspace: AmpWorkspaceFixture
  authenticatedAmpWorkspace: AmpWorkspaceFixture
  /** An agent in LeapMux's default Ask mode, which raises a banner for each call. */
  askingAmpWorkspace: AmpWorkspaceFixture
}>({
  ampWorkspace: async ({ leapmuxServer }, use) => {
    const workingDir = createTestDirectory('amp-e2e-wd-')
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.AMP, prefix: 'amp-e2e', openOptions: ALLOW_ALL, workingDir: () => workingDir }, async (workspace) => {
      await use({ ...workspace, workingDir })
    })
  },

  authenticatedAmpWorkspace: async ({ page, ampWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ampWorkspace.workspaceId)
    await use(ampWorkspace)
  },

  askingAmpWorkspace: async ({ page, leapmuxServer }, use) => {
    const workingDir = createTestDirectory('amp-e2e-ask-wd-')
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.AMP, prefix: 'amp-e2e-ask', workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  },
})

export { expect }
