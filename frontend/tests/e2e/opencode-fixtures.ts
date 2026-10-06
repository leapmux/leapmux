/**
 * OpenCode-specific e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { nativeContext, OPENCODE_AGENT } from './opencode/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const OPENCODE_E2E_SKIP_REASON: string | null = missingBinaryReason('opencode', 'OpenCode E2E requires opencode CLI on PATH')

export const opencodeTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedOpencodeWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(OPENCODE_E2E_SKIP_REASON),
  authenticatedOpencodeWorkspace: authenticatedAgentWorkspace(OPENCODE_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedOpencodeWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId }))
  },
})

export { expect }
