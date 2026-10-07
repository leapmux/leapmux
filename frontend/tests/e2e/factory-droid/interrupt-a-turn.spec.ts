import { droidTest } from '../droid-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

droidTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

droidTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

droidTest('withdraws a waiting question and accepts the next queued turn', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
