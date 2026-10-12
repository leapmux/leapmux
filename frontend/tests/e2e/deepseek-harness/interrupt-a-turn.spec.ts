import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'

deepseekHarnessTest('cancels native model and command turns without replacing the session', async ({ native }) => {
  await exerciseInterruptTurn(native)
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

deepseekHarnessTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})

deepseekHarnessTest('withdraws a waiting question without replacing the session', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
