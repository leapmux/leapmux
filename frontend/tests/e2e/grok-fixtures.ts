/**
 * Grok Build e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base } from './fixtures'
import { GROK_AGENT, nativeContext } from './grok-build/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const GROK_E2E_SKIP_REASON: string | null = missingBinaryReason('grok', 'Grok Build E2E requires a grok CLI on PATH')

export const grokTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedGrokWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(GROK_E2E_SKIP_REASON),
  authenticatedGrokWorkspace: authenticatedAgentWorkspace(GROK_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId }))
  },
})
