import { copilotTest } from '../copilot-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

copilotTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
