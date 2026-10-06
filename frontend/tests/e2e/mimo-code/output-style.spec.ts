import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { mimoTest } from '../mimo-fixtures'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

mimoTest('proves the missing output-style setting against the live catalog and a native tool', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof: () => exerciseMiMoShellToolExecution(context, { includeFailure: false }) })
})
