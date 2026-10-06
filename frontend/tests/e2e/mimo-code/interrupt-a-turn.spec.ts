import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { mimoTest } from '../mimo-fixtures'

mimoTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

mimoTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
