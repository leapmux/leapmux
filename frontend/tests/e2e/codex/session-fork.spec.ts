import { codexTest } from '../codex-fixtures'
import { exerciseNativeFork } from '../helpers/nativeFork'

codexTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
