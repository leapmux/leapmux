import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { qwenTest } from '../qwen-fixtures'

qwenTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

qwenTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

qwenTest('withdraws a waiting question and keeps its session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
