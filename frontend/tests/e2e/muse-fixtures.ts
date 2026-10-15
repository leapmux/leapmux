import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { MUSE_AGENT, nativeContext } from './muse-code/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const MUSE_E2E_SKIP_REASON: string | null = missingBinaryReason('muse', 'Muse Code E2E requires the native muse executable.')

export const museTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedMuseWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(MUSE_E2E_SKIP_REASON),
  authenticatedMuseWorkspace: authenticatedAgentWorkspace({ ...MUSE_AGENT, openOptions: { optionValues: { permissionMode: 'allowAll' } } }),
  native: async ({ page, modelScript, leapmuxServer, authenticatedMuseWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedMuseWorkspace.workspaceId }))
  },
})
