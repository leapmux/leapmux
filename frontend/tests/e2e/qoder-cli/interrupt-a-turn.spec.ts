import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'

qoderTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

qoderTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

qoderTest('withdraws a waiting question and accepts the next queued turn', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
