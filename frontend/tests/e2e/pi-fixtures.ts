import type { WorkspaceFixture } from './helpers/workspace'
/** Pi fixtures use the shared agent workspace lifetime. */
import { execFileSync } from 'node:child_process'
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

/**
 * Skip Pi E2E tests when the `pi` CLI is not installed. The agent server
 * is what actually contacts Pi's RPC mode; without the binary the agent
 * cannot start and any test would fail before reaching the chat surface.
 */
export const PI_E2E_SKIP_REASON: string | null = (() => {
  try {
    execFileSync('pi', ['--version'], { encoding: 'utf-8' })
    return null
  }
  catch {
    return 'Pi E2E requires pi CLI on PATH (https://github.com/badlogic/pi-mono)'
  }
})()

export const piTest = base.extend<{
  piWorkspace: WorkspaceFixture
  authenticatedPiWorkspace: WorkspaceFixture
}>({
  piWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.PI, prefix: 'pi-e2e' }, use)
  },

  authenticatedPiWorkspace: async ({ page, piWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, piWorkspace.workspaceId)

    await use(piWorkspace)
  },
})

export { expect }
