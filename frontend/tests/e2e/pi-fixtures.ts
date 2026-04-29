/**
 * Pi-specific e2e test fixtures.
 *
 * Pi is a JSONL-over-stdio agent (not ACP, not JSON-RPC), so the fixture
 * mirrors the Codex pattern rather than the shared ACP factory.
 */
import { execFileSync } from 'node:child_process'
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
}

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
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `pi-e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, undefined, {
      agentProvider: AgentProvider.PI,
    })
    const workspaceUrl = `/o/admin/workspace/${workspaceId}`

    await use({ workspaceId, workspaceUrl })

    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  authenticatedPiWorkspace: async ({ page, piWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto(piWorkspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    await use(piWorkspace)
  },
})

export { expect }
