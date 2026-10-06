import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { CURSOR_AGENT, nativeContext } from './cursor/scenarios'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const CURSOR_E2E_SKIP_REASON: string | null = missingBinaryReason('agent', 'Cursor E2E requires an agent CLI on PATH')

export const cursorTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedCursorWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CURSOR_E2E_SKIP_REASON),
  authenticatedCursorWorkspace: authenticatedAgentWorkspace(CURSOR_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedCursorWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId }))
  },
})

export { expect }
