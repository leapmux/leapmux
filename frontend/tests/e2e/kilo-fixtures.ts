/**
 * Kilo-specific e2e test fixtures.
 */
import type { ACPFixtureConfig, CliSkipFixture } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, cliSkipFixture, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

const kiloConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.KILO,
  cliBinary: 'kilo',
  skipMessage: 'Kilo E2E requires a kilo CLI on PATH',
  workspacePrefix: 'kilo-e2e',
}

export const KILO_E2E_SKIP_REASON = detectACPSkipReason(kiloConfig)

export const kiloTest = base.extend<CliSkipFixture & {
  kiloWorkspace: WorkspaceFixture
  authenticatedKiloWorkspace: WorkspaceFixture
}>({
  cliSkip: cliSkipFixture(KILO_E2E_SKIP_REASON),
  kiloWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, kiloConfig, use)
  },

  authenticatedKiloWorkspace: async ({ page, kiloWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, kiloWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
