import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('stops a native model turn and resumes its paused queue', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
})

junieTest('stops an actual native tool and accepts the next queued turn', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
