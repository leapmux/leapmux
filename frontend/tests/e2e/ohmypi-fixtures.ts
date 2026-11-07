import type { AgentWorkspace } from './helpers/workspace'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { nativeContext, OH_MY_PI_AGENT } from './oh-my-pi/scenarios'
import { createProviderTest } from './provider-fixture-factory'

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
  workspaceName: 'authenticatedOhMyPiWorkspace',
  workspaceOptions: { openOptions: OH_MY_PI_YOLO },
}).extend<{
  /** An agent in LeapMux's default `write` mode, which asks before a command runs. */
  askingOhMyPiWorkspace: AgentWorkspace
}>({
  askingOhMyPiWorkspace: authenticatedAgentWorkspace({ ...OH_MY_PI_AGENT, prefix: 'omp-e2e-approve' }),
})
