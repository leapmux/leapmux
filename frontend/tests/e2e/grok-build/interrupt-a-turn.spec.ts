import { grokTest } from '../grok-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

grokTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

grokTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

grokTest('withdraws a waiting question and keeps its session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
