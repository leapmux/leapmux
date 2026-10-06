import { codexTest } from '../codex-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

codexTest('reports a native model error and accepts the next turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
