import { diracTest } from '../dirac-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

diracTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

diracTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

// Dirac's question operation raises an elicitation form, and the form waits in the same banner. The default agent
// runs in YOLO mode, which answers the question itself, so the agent asks as the agent-questions spec does.
diracTest('withdraws a waiting question form and accepts the next queued turn', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  await exerciseControlInterrupt(context, { control: 'question' })
})
