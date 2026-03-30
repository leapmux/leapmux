/**
 * Shared factory for ACP-based e2e test fixtures.
 * Eliminates duplicated fixture boilerplate across Copilot, Gemini, and OpenCode.
 */
import { execFileSync } from 'node:child_process'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

export { AgentProvider } from '../../src/generated/leapmux/v1/agent_pb'

export interface ACPFixtureConfig {
  agentProvider: number
  /** CLI binary name to check on PATH (e.g. 'copilot', 'gemini'). Null skips the check. */
  cliBinary?: string
  /** Skip message when the CLI binary is not found. */
  skipMessage?: string
  /** Prefix for workspace names (e.g. 'copilot-e2e', 'gemini-e2e', 'opencode-e2e'). */
  workspacePrefix: string
  /** Optional explicit model to use when opening the agent. */
  model?: string
}

export interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
}

export function detectACPSkipReason(config: ACPFixtureConfig): string | null {
  if (!config.cliBinary)
    return null
  try {
    execFileSync(config.cliBinary, ['--version'], { encoding: 'utf-8' }).trim()
  }
  catch {
    return config.skipMessage || `E2E requires ${config.cliBinary} CLI on PATH`
  }
  return null
}

export async function createACPWorkspace(
  leapmuxServer: { hubUrl: string, adminToken: string, adminOrgId: string, workerId: string },
  config: ACPFixtureConfig,
  use: (fixture: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
  const workspaceId = await createWorkspaceViaAPI(
    hubUrl,
    adminToken,
    `${config.workspacePrefix}-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
    adminOrgId,
  )
  await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, undefined, {
    agentProvider: config.agentProvider,
    model: config.model,
  })
  const workspaceUrl = `/o/admin/workspace/${workspaceId}`

  await use({ workspaceId, workspaceUrl })

  try {
    await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
  }
  catch { /* best effort */ }
}

export async function authenticateACPWorkspace(
  page: Parameters<typeof loginViaToken>[0] & { goto: (url: string) => Promise<void> },
  workspace: WorkspaceFixture,
  adminToken: string,
  use: (fixture: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  await loginViaToken(page, adminToken)
  await page.goto(workspace.workspaceUrl)
  await waitForWorkspaceReady(page)

  await use(workspace)
}
