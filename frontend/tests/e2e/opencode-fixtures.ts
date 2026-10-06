/**
 * OpenCode-specific e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const OPENCODE_E2E_SKIP_REASON: string | null = missingBinaryReason('opencode', 'OpenCode E2E requires opencode CLI on PATH')

/** How an OpenCode agent opens. */
export const OPENCODE_AGENT: ProviderAgent = { provider: AgentProvider.OPENCODE, prefix: 'opencode-e2e' }

export const opencodeTest = base.extend<CliSkipFixture & {
  authenticatedOpencodeWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(OPENCODE_E2E_SKIP_REASON),
  authenticatedOpencodeWorkspace: authenticatedAgentWorkspace(OPENCODE_AGENT),
})

export { expect }
