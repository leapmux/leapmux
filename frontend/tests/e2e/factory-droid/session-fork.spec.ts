import { droidTest } from '../droid-fixtures'
import { exerciseNativeFork } from '../helpers/nativeFork'

droidTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
