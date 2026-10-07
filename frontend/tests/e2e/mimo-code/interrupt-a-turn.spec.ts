import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { mimoTest } from '../mimo-fixtures'

mimoTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

mimoTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

mimoTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})

mimoTest('withdraws a waiting question and keeps its session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
