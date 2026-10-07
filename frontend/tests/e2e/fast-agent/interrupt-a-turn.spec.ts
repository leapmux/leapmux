import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

fastAgentTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

fastAgentTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

// Fast Agent asks no native question, so a permission request holds the turn. It asks before each shell command.
fastAgentTest('withdraws a waiting permission and accepts the next queued turn', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'permission' })
})
