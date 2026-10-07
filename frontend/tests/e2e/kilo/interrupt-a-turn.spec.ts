import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { kiloTest } from '../kilo-fixtures'

kiloTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

kiloTest('stops an actual native tool and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

kiloTest('withdraws a waiting question and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
