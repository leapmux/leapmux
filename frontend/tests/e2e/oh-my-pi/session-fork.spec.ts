import { exerciseNativeFork } from '../helpers/nativeFork'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('forks conversation history into an isolated native session', async ({ native }) => {
  await exerciseNativeFork(native)
})
