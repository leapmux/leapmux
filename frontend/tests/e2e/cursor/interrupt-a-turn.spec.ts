import { cursorTest } from '../cursor-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

cursorTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native)
})
