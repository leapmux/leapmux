import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

fastAgentTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})
