import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { kimiTest } from '../kimi-fixtures'

kimiTest('proves the missing output-style setting against the live catalog and a native tool', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
