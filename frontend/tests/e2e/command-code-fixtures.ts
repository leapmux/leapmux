import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { COMMAND_CODE_AGENT, nativeContext } from './command-code/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'

export const COMMAND_CODE_E2E_SKIP_REASON: string | null = missingBinaryReason('command-code', 'Command Code E2E requires the native command-code executable.')

export const commandCodeTest = base.extend<CliSkipFixture & NativeFixture & {
  /** An agent in bypass mode, which runs every tool call at once. */
  authenticatedCommandCodeWorkspace: AgentWorkspace
  /**
   * An agent in Command Code's default mode. It refuses a write that needs a permission, and it raises no dialog,
   * so it does not ask as the `asking*` workspaces of other providers do.
   */
  refusingCommandCodeWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(COMMAND_CODE_E2E_SKIP_REASON),
  authenticatedCommandCodeWorkspace: authenticatedAgentWorkspace({ ...COMMAND_CODE_AGENT, openOptions: { optionValues: { permissionMode: 'bypass' } } }),
  refusingCommandCodeWorkspace: authenticatedAgentWorkspace({ ...COMMAND_CODE_AGENT, prefix: 'command-code-e2e-default', openOptions: { optionValues: { permissionMode: 'default' } } }),
  native: async ({ page, modelScript, leapmuxServer, authenticatedCommandCodeWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId }))
  },
})
