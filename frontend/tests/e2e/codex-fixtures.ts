/**
 * Codex-specific e2e test fixtures.
 * Extends the base fixtures with a Codex agent instead of Claude Code.
 */
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, openWorkspace } from './helpers/ui'

interface WorkspaceFixture {
  workspaceId: string
}

export const codexTest = base.extend<{
  codexWorkspace: WorkspaceFixture
  authenticatedCodexWorkspace: WorkspaceFixture
}>({
  codexWorkspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `codex-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, undefined, {
      agentProvider: AgentProvider.CODEX,
    })
    await use({ workspaceId })

    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  authenticatedCodexWorkspace: async ({ page, codexWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, codexWorkspace.workspaceId)

    await use(codexWorkspace)
  },
})

export { expect }
