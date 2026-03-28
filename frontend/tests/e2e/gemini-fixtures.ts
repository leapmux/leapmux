/**
 * Gemini-specific e2e test fixtures.
 * Extends the base fixtures with a Gemini agent instead of Claude Code.
 */
import { execFileSync } from 'node:child_process'
import { test as base, expect } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// AgentProvider.GEMINI_CLI = 3
const AGENT_PROVIDER_GEMINI_CLI = 3

function geminiE2ESkipReason(): string | null {
  try {
    execFileSync('gemini', ['--version'], { encoding: 'utf-8' }).trim()
  }
  catch {
    return 'Gemini E2E requires a gemini CLI on PATH'
  }

  return null
}

export const GEMINI_E2E_SKIP_REASON = geminiE2ESkipReason()

interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
}

export const geminiTest = base.extend<{
  geminiWorkspace: WorkspaceFixture
  authenticatedGeminiWorkspace: WorkspaceFixture
}>({
  geminiWorkspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `gemini-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, undefined, {
      agentProvider: AGENT_PROVIDER_GEMINI_CLI,
    })
    const workspaceUrl = `/o/admin/workspace/${workspaceId}`

    await use({ workspaceId, workspaceUrl })

    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  authenticatedGeminiWorkspace: async ({ page, geminiWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto(geminiWorkspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    await use(geminiWorkspace)
  },
})

export { expect }
