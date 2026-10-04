import { droidTest } from '../droid-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

droidTest('stops a native model turn and resumes its paused queue', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
})

droidTest('stops an actual native tool and accepts the next queued turn', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
