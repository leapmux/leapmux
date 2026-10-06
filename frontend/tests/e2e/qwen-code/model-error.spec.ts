import { exerciseModelError } from '../helpers/nativeModelError'
import { qwenTest } from '../qwen-fixtures'

qwenTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
