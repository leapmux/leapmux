import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'

junieTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

junieTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
