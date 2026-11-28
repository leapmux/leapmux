import { exerciseNativeFork } from '../helpers/nativeFork'
import { piTest } from '../pi-fixtures'

piTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
