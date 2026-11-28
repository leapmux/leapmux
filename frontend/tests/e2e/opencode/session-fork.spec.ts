import { exerciseNativeFork } from '../helpers/nativeFork'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
