import { exerciseNativeFork } from '../helpers/nativeFork'
import { kiloTest } from '../kilo-fixtures'

kiloTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
