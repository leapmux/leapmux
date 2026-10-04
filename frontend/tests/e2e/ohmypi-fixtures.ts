import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

const OH_MY_PI_MISSING_REASON = 'Oh My Pi E2E requires the omp CLI on PATH (https://github.com/can1357/oh-my-pi)'

/**
 * Skip the Oh My Pi specs when the worker cannot start the `omp` CLI. The worker is
 * what starts `omp --mode rpc-ui`; without the binary the agent cannot start, and a
 * test would fail before it reaches the chat surface.
 *
 * The check finds the file first without running it, which also refuses a mise
 * shim (see `helpers/binaryOnPath.ts`). Only then does it run that file's
 * `--version`, to refuse an install that does not start.
 */
const OH_MY_PI = lookupBinary('omp', OH_MY_PI_MISSING_REASON)
export const OH_MY_PI_E2E_SKIP_REASON: string | null = OH_MY_PI.path === null
  ? OH_MY_PI.skipReason
  : (versionOutput(OH_MY_PI.path) === null ? OH_MY_PI_MISSING_REASON : null)

/**
 * The agent opens in omp's `yolo` approval mode, which runs every tool without asking.
 *
 * LeapMux opens a new omp session in `write` mode, which asks before every command. A
 * spec that tests what a tool DRAWS would otherwise answer an approval before each
 * call; the approval spec opens its own workspace in `write` mode on purpose.
 */
const YOLO = { optionValues: { permissionMode: 'yolo' } }

export const ohMyPiTest = base.extend<{
  ohMyPiWorkspace: WorkspaceFixture
  authenticatedOhMyPiWorkspace: WorkspaceFixture
  /** An agent in LeapMux's default `write` mode, which asks before a command runs. */
  approvingOhMyPiWorkspace: WorkspaceFixture
}>({
  ohMyPiWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.OH_MY_PI, prefix: 'omp-e2e', openOptions: YOLO }, use)
  },

  authenticatedOhMyPiWorkspace: async ({ page, ohMyPiWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ohMyPiWorkspace.workspaceId)
    await use(ohMyPiWorkspace)
  },

  approvingOhMyPiWorkspace: async ({ page, leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.OH_MY_PI, prefix: 'omp-e2e-approve' }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use(workspace)
    })
  },
})

export { expect }
