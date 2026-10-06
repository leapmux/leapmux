import { codexTest } from '../codex-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

codexTest('reopens the native session and restores its saved UI transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
