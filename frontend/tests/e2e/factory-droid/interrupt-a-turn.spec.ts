import { droidTest } from '../droid-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

droidTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

droidTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
