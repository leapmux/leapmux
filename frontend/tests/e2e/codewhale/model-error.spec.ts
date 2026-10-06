import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

codewhaleTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
