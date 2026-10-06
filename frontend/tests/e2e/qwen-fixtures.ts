/**
 * Qwen Code e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'
import { nativeContext } from './qwen-code/scenarios'

export const QWEN_E2E_SKIP_REASON: string | null = missingBinaryReason('qwen', 'Qwen Code E2E requires a qwen CLI on PATH')

/** How a Qwen Code agent opens. */
export const QWEN_AGENT: ProviderAgent = { provider: AgentProvider.QWEN_CODE, prefix: 'qwen-e2e' }

export const qwenTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedQwenWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(QWEN_E2E_SKIP_REASON),
  authenticatedQwenWorkspace: authenticatedAgentWorkspace(QWEN_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId }))
  },
})

export { expect }
