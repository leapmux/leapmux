/**
 * Copilot-specific e2e test fixtures.
 * Extends the base fixtures with a Copilot agent instead of Claude Code.
 */
import { execFileSync } from 'node:child_process'
import { test as base, expect } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// AgentProvider.COPILOT_CLI = 5
const AGENT_PROVIDER_COPILOT_CLI = 5

function copilotE2ESkipReason(): string | null {
  try {
    execFileSync('copilot', ['--version'], { encoding: 'utf-8' }).trim()
  }
  catch {
    return 'Copilot E2E requires a copilot CLI on PATH'
  }

  return null
}

export const COPILOT_E2E_SKIP_REASON = copilotE2ESkipReason()

interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
}

export const copilotTest = base.extend<{
  copilotWorkspace: WorkspaceFixture
  authenticatedCopilotWorkspace: WorkspaceFixture
}>({
  copilotWorkspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `copilot-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, undefined, {
      agentProvider: AGENT_PROVIDER_COPILOT_CLI,
    })
    const workspaceUrl = `/o/admin/workspace/${workspaceId}`

    await use({ workspaceId, workspaceUrl })

    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  authenticatedCopilotWorkspace: async ({ page, copilotWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto(copilotWorkspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    await use(copilotWorkspace)
  },
})

export { expect }
