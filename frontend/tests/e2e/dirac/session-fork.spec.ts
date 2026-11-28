import { diracTest } from '../dirac-fixtures'
import { exerciseNativeFork } from '../helpers/nativeFork'

diracTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
