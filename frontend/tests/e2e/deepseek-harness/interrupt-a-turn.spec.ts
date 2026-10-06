import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

deepseekHarnessTest('cancels native model and command turns without replacing the session', async ({ native }) => {
  await exerciseInterruptTurn(native)
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
