/**
 * Goose-specific e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { expect } from '@playwright/test'
import { test as base } from './fixtures'
import { GOOSE_AGENT, nativeContext } from './goose/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const GOOSE_E2E_SKIP_REASON: string | null = missingBinaryReason('goose', 'Goose E2E requires a goose CLI on PATH')

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
