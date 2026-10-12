import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

fastAgentTest('clears native context while keeping the saved LeapMux rows', async ({ native }) => {
  await exerciseSessionReset(native)
})
