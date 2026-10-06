import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('stops a native model turn and resumes its paused queue', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
})

qoderTest('stops an actual native tool and accepts the next queued turn', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
