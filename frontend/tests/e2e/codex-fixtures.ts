/**
 * Codex-specific e2e test fixtures.
 * Extends the base fixtures with a Codex agent instead of Claude Code.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { CODEX_EXECUTABLE } from '../../src/generated/contracts/codex-protocol'
import { CODEX_AGENT, nativeContext } from './codex/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

/**
 * The Worker starts the first name of the contract that its search path holds, so the specs run when the path
 * holds any one of them.
 */
export const CODEX_E2E_SKIP_REASON: string | null = missingBinaryReason(
  Object.values(CODEX_EXECUTABLE),
  `Codex E2E requires a Codex CLI on PATH under one of these names: ${Object.values(CODEX_EXECUTABLE).join(', ')}`,
)

export const codexTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedCodexWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CODEX_E2E_SKIP_REASON),
  authenticatedCodexWorkspace: authenticatedAgentWorkspace(CODEX_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedCodexWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodexWorkspace.workspaceId }))
  },
})
