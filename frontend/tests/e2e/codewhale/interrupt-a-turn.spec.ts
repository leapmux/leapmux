import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

codewhaleTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

codewhaleTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
