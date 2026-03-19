/**
 * Codex-specific e2e test fixtures.
 * Extends the base fixtures with a Codex agent instead of Claude Code.
 */
import { test as base, expect } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// AgentProvider.CODEX = 2
const AGENT_PROVIDER_CODEX = 2

interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
}

export const codexTest = base.extend<{
  codexWorkspace: WorkspaceFixture
  authenticatedCodexWorkspace: WorkspaceFixture
}>({
  codexWorkspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `codex-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, undefined, {
      agentProvider: AGENT_PROVIDER_CODEX,
    })
    const workspaceUrl = `/o/admin/workspace/${workspaceId}`

    await use({ workspaceId, workspaceUrl })

    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  authenticatedCodexWorkspace: async ({ page, codexWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto(codexWorkspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    await use(codexWorkspace)
  },
})

export { expect }
