import { copilotTest } from '../copilot-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

copilotTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})
