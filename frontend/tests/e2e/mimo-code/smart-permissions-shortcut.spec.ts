import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { mimoTest } from '../mimo-fixtures'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

mimoTest('proves the absent shortcut after a native shell operation', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof: () => exerciseMiMoShellToolExecution(context, { includeFailure: false }) })
})
