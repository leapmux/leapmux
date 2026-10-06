import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native)
})
