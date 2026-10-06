import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const CURSOR_E2E_SKIP_REASON: string | null = missingBinaryReason('agent', 'Cursor E2E requires an agent CLI on PATH')

/** How a Cursor agent opens. */
export const CURSOR_AGENT: ProviderAgent = { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e' }

export const cursorTest = base.extend<CliSkipFixture & {
  authenticatedCursorWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CURSOR_E2E_SKIP_REASON),
  authenticatedCursorWorkspace: authenticatedAgentWorkspace(CURSOR_AGENT),
})

export { expect }
