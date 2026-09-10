import type { WorkspaceFixture } from './helpers/workspace'
/**
 * Codex-specific e2e test fixtures.
 * Extends the base fixtures with a Codex agent instead of Claude Code.
 */
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export const codexTest = base.extend<{
  codexWorkspace: WorkspaceFixture
  authenticatedCodexWorkspace: WorkspaceFixture
}>({
  codexWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.CODEX, prefix: 'codex-e2e' }, use)
  },

  authenticatedCodexWorkspace: async ({ page, codexWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, codexWorkspace.workspaceId)

    await use(codexWorkspace)
  },
})

export { expect }
