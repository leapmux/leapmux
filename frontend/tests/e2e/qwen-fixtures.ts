/**
 * Qwen Code e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { expect } from '@playwright/test'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'
import { nativeContext, QWEN_AGENT } from './qwen-code/scenarios'

export const QWEN_E2E_SKIP_REASON: string | null = missingBinaryReason('qwen', 'Qwen Code E2E requires a qwen CLI on PATH')

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
