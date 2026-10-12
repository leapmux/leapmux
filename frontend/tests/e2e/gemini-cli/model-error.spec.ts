import { geminiTest } from '../gemini-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

geminiTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
