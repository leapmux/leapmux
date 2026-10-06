/**
 * Codex-specific e2e test fixtures.
 * Extends the base fixtures with a Codex agent instead of Claude Code.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeContext } from './codex/scenarios'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const CODEX_E2E_SKIP_REASON: string | null = missingBinaryReason('codex', 'Codex E2E requires the codex CLI on PATH')

/** How a Codex agent opens. */
export const CODEX_AGENT: ProviderAgent = { provider: AgentProvider.CODEX, prefix: 'codex-e2e' }

export const codexTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedCodexWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CODEX_E2E_SKIP_REASON),
  authenticatedCodexWorkspace: authenticatedAgentWorkspace(CODEX_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedCodexWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodexWorkspace.workspaceId }))
  },
})

export { expect }
