import type { AgentWorkspace } from './helpers/workspace'
import { COMMAND_CODE_AGENT, nativeContext } from './command-code/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'

export const COMMAND_CODE_E2E_SKIP_REASON: string | null = missingBinaryReason('command-code', 'Command Code E2E requires the native command-code executable.')

export const commandCodeTest = createProviderTest({
  agent: COMMAND_CODE_AGENT,
  nativeContext,
  skipReason: COMMAND_CODE_E2E_SKIP_REASON,
  /** An agent in bypass mode, which runs every tool call at once. */
  workspaceName: 'authenticatedCommandCodeWorkspace',
  workspaceOptions: { openOptions: { optionValues: { permissionMode: 'bypass' } } },
}).extend<{
  /**
   * An agent in Command Code's default mode.
   * It refuses a write that needs permission and raises no dialog.
   * Other providers' `asking*` workspaces raise a permission dialog instead.
   */
  refusingCommandCodeWorkspace: AgentWorkspace
}>({
  refusingCommandCodeWorkspace: authenticatedAgentWorkspace({ ...COMMAND_CODE_AGENT, prefix: 'command-code-e2e-default', openOptions: { optionValues: { permissionMode: 'default' } } }),
})
