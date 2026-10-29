import type { AgentWorkspace } from './helpers/workspace'
import { lookupBinary, versionOutput } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { nativeContext, OH_MY_PI_AGENT } from './oh-my-pi/scenarios'
import { createProviderTest } from './provider-fixture-factory'

const OH_MY_PI_MISSING_REASON = 'Oh My Pi E2E requires the omp CLI on PATH (https://github.com/can1357/oh-my-pi)'

/**
 * Skip the Oh My Pi specs when the worker cannot start the `omp` CLI.
 * The worker starts `omp --mode rpc-ui`.
 * Without the binary, the agent cannot start and the test fails before it reaches the chat view.
 *
 * The check finds the file first without running it, which also refuses a mise
 * shim (see `helpers/binaryOnPath.ts`). Only then does it run that file's
 * `--version`, to refuse an install that does not start.
 */
const OH_MY_PI = lookupBinary('omp', OH_MY_PI_MISSING_REASON)
export const OH_MY_PI_E2E_SKIP_REASON: string | null = OH_MY_PI.path === null
  ? OH_MY_PI.skipReason
  : (versionOutput(OH_MY_PI.path) === null
      ? OH_MY_PI_MISSING_REASON
      : null)

/**
 * The agent opens in omp's `yolo` approval mode, which runs every tool without asking.
 *
 * LeapMux opens a new omp session in `write` mode, which asks before every command. A
 * spec that tests tool rendering would otherwise answer an approval before each call.
 * The approval spec opens its own workspace in `write` mode.
 */
export const OH_MY_PI_YOLO = {
  optionValues: { permissionMode: 'yolo' },
}

export const ohMyPiTest = createProviderTest({
  agent: OH_MY_PI_AGENT,
  nativeContext,
  skipReason: OH_MY_PI_E2E_SKIP_REASON,
  workspaceName: 'authenticatedOhMyPiWorkspace',
  workspaceOptions: { openOptions: OH_MY_PI_YOLO },
}).extend<{
  /** An agent in LeapMux's default `write` mode, which asks before a command runs. */
  askingOhMyPiWorkspace: AgentWorkspace
}>({
  askingOhMyPiWorkspace: authenticatedAgentWorkspace({ ...OH_MY_PI_AGENT, prefix: 'omp-e2e-approve' }),
})
