import { exerciseNativeFork } from '../helpers/nativeFork'
import { kiroTest } from '../kiro-fixtures'

kiroTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
