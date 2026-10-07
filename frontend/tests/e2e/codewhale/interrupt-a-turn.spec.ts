import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'

codewhaleTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

codewhaleTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

codewhaleTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})

// Codewhale defers `request_user_input`: its first call loads the schema and runs nothing.
codewhaleTest('withdraws a waiting question and keeps its session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question', deferredTool: true })
})
