import { claudeTest } from '../claude-fixtures'
import { exerciseNativeFork } from '../helpers/nativeFork'

claudeTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
