/**
 * Kilo-specific e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { nativeContext } from './kilo/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const KILO_E2E_SKIP_REASON: string | null = missingBinaryReason('kilo', 'Kilo E2E requires a kilo CLI on PATH')

/** How a Kilo agent opens. */
export const KILO_AGENT: ProviderAgent = { provider: AgentProvider.KILO, prefix: 'kilo-e2e' }

export const kiloTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedKiloWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(KILO_E2E_SKIP_REASON),
  authenticatedKiloWorkspace: authenticatedAgentWorkspace(KILO_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedKiloWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId }))
  },
})

export { expect }
