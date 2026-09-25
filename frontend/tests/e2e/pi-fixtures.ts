import type { WorkspaceFixture } from './helpers/workspace'
/** Pi fixtures use the shared agent workspace lifetime. */
import { AgentProvider } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

const PI_MISSING_REASON = 'Pi E2E requires pi CLI on PATH (https://github.com/badlogic/pi-mono)'

/**
 * Skip Pi E2E tests when the worker cannot start the `pi` CLI. The agent server
 * is what actually contacts Pi's RPC mode; without the binary the agent
 * cannot start and any test would fail before reaching the chat surface.
 *
 * The check finds the file first without running it, which also refuses a mise
 * shim (see `helpers/binaryOnPath.ts`). Only then does it run that file's
 * `--version`, to refuse an install that does not start.
 */
const PI = lookupBinary('pi', PI_MISSING_REASON)
export const PI_E2E_SKIP_REASON: string | null = PI.path === null
  ? PI.skipReason
  : (versionOutput(PI.path) === null ? PI_MISSING_REASON : null)

export const piTest = base.extend<{
  piWorkspace: WorkspaceFixture
  authenticatedPiWorkspace: WorkspaceFixture
}>({
  piWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.PI, prefix: 'pi-e2e' }, use)
  },

  authenticatedPiWorkspace: async ({ page, piWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, piWorkspace.workspaceId)

    await use(piWorkspace)
  },
})

export { expect }
