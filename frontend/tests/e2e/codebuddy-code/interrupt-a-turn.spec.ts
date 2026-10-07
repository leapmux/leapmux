import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

codebuddyTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

codebuddyTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

// CodeBuddy Code asks no native question, so a permission request holds the turn. The asking agent raises one.
codebuddyTest('withdraws a waiting permission and accepts the next queued turn', async ({ askingCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingCodebuddyWorkspace.workspaceId })
  await exerciseControlInterrupt(context, { control: 'permission' })
})
