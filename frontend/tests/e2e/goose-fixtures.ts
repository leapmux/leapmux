/**
 * Goose-specific e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { nativeContext } from './goose/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const GOOSE_E2E_SKIP_REASON: string | null = missingBinaryReason('goose', 'Goose E2E requires a goose CLI on PATH')

/** How a Goose agent opens. */
export const GOOSE_AGENT: ProviderAgent = { provider: AgentProvider.GOOSE, prefix: 'goose-e2e' }

export const gooseTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedGooseWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(GOOSE_E2E_SKIP_REASON),
  authenticatedGooseWorkspace: authenticatedAgentWorkspace(GOOSE_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedGooseWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId }))
  },
})

export { expect }
