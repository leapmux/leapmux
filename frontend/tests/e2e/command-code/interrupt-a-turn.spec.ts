import { commandCodeTest } from '../command-code-fixtures'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'

commandCodeTest('interrupts actual native model and tool turns without replacing the session', async ({ native }) => {
  await exerciseInterruptTurn(native)
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

commandCodeTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})
