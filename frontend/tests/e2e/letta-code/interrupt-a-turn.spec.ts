import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('stops a native model turn and resumes its paused queue', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
})

lettaTest('stops an actual native tool and accepts the next queued turn', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
