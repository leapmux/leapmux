import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

codebuddyTest('stops a native model turn and resumes its paused queue', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
})

codebuddyTest('stops an actual native tool and accepts the next queued turn', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
