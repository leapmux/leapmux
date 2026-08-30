/**
 * Shared factory for ACP-based e2e test fixtures.
 * Eliminates duplicated fixture boilerplate across Copilot, Cursor, and OpenCode.
 */
import { execFileSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { loginViaToken, openWorkspace } from './helpers/ui'

export { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'

export interface ACPFixtureConfig {
  agentProvider: number
  /** CLI binary name to check on PATH (e.g. 'copilot', 'cursor-agent'). Null skips the check. */
  cliBinary?: string
  /** Skip message when the CLI binary is not found. */
  skipMessage?: string
  /** Prefix for workspace names (e.g. 'copilot-e2e', 'cursor-e2e', 'opencode-e2e'). */
  workspacePrefix: string
  /** Optional explicit model to use when opening the agent. */
  model?: string
}

export interface WorkspaceFixture {
  workspaceId: string
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
  leapmuxServer: { hubUrl: string, adminToken: string, workerId: string },
  config: ACPFixtureConfig,
  use: (fixture: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  const { hubUrl, adminToken, workerId } = leapmuxServer
  const workspaceId = await createWorkspaceViaAPI(
    hubUrl,
    adminToken,
    `${config.workspacePrefix}-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
  )
  // Use a temp working directory instead of the home dir (the empty-workingDir
  // fallback). Some ACP agents (OpenCode) hang when started from the home
  // directory, so always launch from an empty temp dir.
  const workingDir = mkdtempSync(join(tmpdir(), `${config.workspacePrefix}-wd-`))
  await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, workingDir, {
    agentProvider: config.agentProvider,
    model: config.model,
  })
  await use({ workspaceId })

  try {
    await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
  }
  catch { /* best effort */ }
}

export async function authenticateACPWorkspace(
  // Playwright's `Page` (loginViaToken's first param) already provides `goto`.
  // Intersecting an explicit `goto` returning Promise<void> contradicts Page's
  // own `goto` (Promise<Response | null>), so use the param type as-is.
  page: Parameters<typeof loginViaToken>[0],
  workspace: WorkspaceFixture,
  adminToken: string,
  use: (fixture: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  await loginViaToken(page, adminToken)
  await openWorkspace(page, workspace.workspaceId)

  await use(workspace)
}
