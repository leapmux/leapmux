import { diracTest } from '../dirac-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

diracTest('stops a native model turn and resumes its paused queue', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
})

diracTest('stops an actual native tool and accepts the next queued turn', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
