/**
 * Codex-specific e2e test fixtures.
 * Extends the base fixtures with a Codex agent instead of Claude Code.
 */
import type { CliSkipFixture } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, cliSkipFixture } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export const CODEX_E2E_SKIP_REASON: string | null = missingBinaryReason('codex', 'Codex E2E requires the codex CLI on PATH')

export const codexTest = base.extend<CliSkipFixture & {
  codexWorkspace: WorkspaceFixture
  authenticatedCodexWorkspace: WorkspaceFixture
}>({
  cliSkip: cliSkipFixture(CODEX_E2E_SKIP_REASON),
  codexWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.CODEX, prefix: 'codex-e2e' }, use)
  },

  authenticatedCodexWorkspace: async ({ page, codexWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, codexWorkspace.workspaceId)

    await use(codexWorkspace)
  },
})

export { expect }
