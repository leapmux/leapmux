import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { museTest } from '../muse-fixtures'

museTest('stops a native model request and accepts the next prompt', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

museTest('stops a running native command and accepts the next prompt', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

museTest('keeps the interrupted native text after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})
