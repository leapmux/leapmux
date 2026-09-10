/**
 * Share workspace fixtures for Agent Client Protocol (ACP) providers that open agents through the API.
 * Search for createACPWorkspace under frontend/tests/e2e to find its callers.
 */
import type { AgentProvider as AgentProviderEnum } from '../../src/generated/proto/leapmux/v1/agent_pb'
import type { WorkspaceFixture } from './helpers/workspace'
import { execFileSync } from 'node:child_process'

import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'

/**
 * Provider configuration for CLI detection and workspace prefixes.
 * createACPWorkspace reads model and effort settings from REAL_AGENT_E2E_SETTINGS through agentProvider.
 * Keeping those settings outside this configuration prevents one provider from receiving another provider model.
 */
export interface ACPFixtureConfig {
  agentProvider: AgentProviderEnum
  /** CLI binary name to check on PATH (e.g. 'copilot', 'agent'). Omit it to skip the check. */
  cliBinary?: string
  /** Skip message when the CLI binary is not found. */
  skipMessage?: string
  /** Prefix for workspace names (e.g. 'copilot-e2e', 'cursor-e2e', 'opencode-e2e'). */
  workspacePrefix: string
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
  await withAgentWorkspace(leapmuxServer, {
    provider: config.agentProvider,
    prefix: config.workspacePrefix,
  }, use)
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
