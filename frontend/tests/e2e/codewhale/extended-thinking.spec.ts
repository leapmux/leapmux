import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest('proves the missing extended-thinking setting against the live catalog and a native tool', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await expectMissingOptionGroup(context, { groupId: 'thinking', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
