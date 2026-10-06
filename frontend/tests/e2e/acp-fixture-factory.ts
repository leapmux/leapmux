/**
 * Share workspace fixtures for Agent Client Protocol (ACP) providers that open agents through the API.
 * Search for createACPWorkspace under frontend/tests/e2e to find its callers.
 */
import type { TestFixture } from '@playwright/test'
import type { AgentProvider as AgentProviderEnum } from '../../src/generated/proto/leapmux/v1/agent_pb'
import type { WorkspaceFixture } from './helpers/workspace'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'

/**
 * Provider configuration for CLI detection and workspace prefixes.
 * createACPWorkspace reads model and effort settings from AGENT_E2E_SETTINGS through agentProvider.
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
  /**
   * Create the agent's working directory. Omit it for a fresh private directory
   * of the run; see `withAgentWorkspace`.
   */
  workingDir?: () => string
}

/** The fixture of {@link cliSkipFixture}, for the type parameter of a provider's `extend` call. */
export interface CliSkipFixture {
  cliSkip: void
}

/**
 * An automatic fixture that skips each test of one provider when the E2E run cannot start the provider's CLI.
 *
 * A missing CLI is a property of the machine, not of model behavior, so every spec of the provider skips with the
 * same reason. Each provider test object registers it under the name `cliSkip`. An automatic fixture runs before
 * the fixtures that a test asks for, so the test skips before a workspace fixture starts an agent.
 */
export function cliSkipFixture(reason: string | null): [TestFixture<void, object>, { auto: true }] {
  if (reason !== null && reason.trim() === '')
    throw new Error('A provider skip needs the reason that its CLI is missing.')
  // Playwright reads the fixture names from the first parameter, which must be an object pattern.
  // eslint-disable-next-line no-empty-pattern
  return [async ({}, use, testInfo) => {
    testInfo.skip(reason !== null, reason ?? '')
    await use()
  }, { auto: true }]
}

/**
 * The reason to skip the provider's specs, or null when its CLI is on PATH. The
 * check runs nothing: `<cli> --version` would run in the developer's own HOME, and
 * some agents write their configuration directory on every start (see
 * `helpers/binaryOnPath.ts`).
 */
export function detectACPSkipReason(config: ACPFixtureConfig): string | null {
  if (!config.cliBinary)
    return null
  return missingBinaryReason(config.cliBinary, config.skipMessage || `E2E requires ${config.cliBinary} CLI on PATH`)
}

export async function createACPWorkspace(
  leapmuxServer: { hubUrl: string, adminToken: string, workerId: string },
  config: ACPFixtureConfig,
  use: (fixture: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  await withAgentWorkspace(leapmuxServer, {
    provider: config.agentProvider,
    prefix: config.workspacePrefix,
    ...(config.workingDir ? { workingDir: config.workingDir } : {}),
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
