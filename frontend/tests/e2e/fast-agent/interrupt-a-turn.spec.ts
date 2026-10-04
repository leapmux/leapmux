import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

fastAgentTest('stops a native model turn and resumes its paused queue', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
})

fastAgentTest('stops an actual native tool and accepts the next queued turn', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
