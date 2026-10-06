import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

deepseekHarnessTest('reopens a native session and restores its stored Worker transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
