import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

fastAgentTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

fastAgentTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
