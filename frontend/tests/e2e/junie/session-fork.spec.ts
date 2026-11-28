import { exerciseNativeFork } from '../helpers/nativeFork'
import { junieTest } from '../junie-fixtures'

junieTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
