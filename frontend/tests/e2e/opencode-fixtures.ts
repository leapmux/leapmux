/**
 * OpenCode-specific e2e test fixtures.
 * Extends the base fixtures with an OpenCode agent instead of Claude Code.
 */
import { test as base, expect } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// AgentProvider.OPENCODE = 3
const AGENT_PROVIDER_OPENCODE = 3

interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
}

export const opencodeTest = base.extend<{
  opencodeWorkspace: WorkspaceFixture
  authenticatedOpencodeWorkspace: WorkspaceFixture
}>({
  opencodeWorkspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `opencode-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, undefined, {
      agentProvider: AGENT_PROVIDER_OPENCODE,
    })
    const workspaceUrl = `/o/admin/workspace/${workspaceId}`

    await use({ workspaceId, workspaceUrl })

    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  authenticatedOpencodeWorkspace: async ({ page, opencodeWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto(opencodeWorkspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    await use(opencodeWorkspace)
  },
})

export { expect }
