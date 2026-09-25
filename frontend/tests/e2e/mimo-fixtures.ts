/**
 * MiMo Code E2E fixtures, on the shared agent workspace lifetime.
 *
 * The skip check looks for the `mimo` binary without running it: MiMo writes a
 * skeleton configuration file on each start, and this check runs with the
 * developer's own HOME. See `helpers/binaryOnPath.ts`.
 */
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export const MIMO_E2E_SKIP_REASON: string | null = missingBinaryReason('mimo', 'MiMo Code E2E requires the mimo CLI on PATH')

export const mimoTest = base.extend<{
  mimoWorkspace: WorkspaceFixture
  authenticatedMiMoWorkspace: WorkspaceFixture
}>({
  mimoWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.MIMO_CODE, prefix: 'mimo-e2e' }, use)
  },

  authenticatedMiMoWorkspace: async ({ page, mimoWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, mimoWorkspace.workspaceId)
    await use(mimoWorkspace)
  },
})

export { expect }
