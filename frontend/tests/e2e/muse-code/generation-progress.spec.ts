import { exerciseTokenProgress } from '../helpers/generationProgress'
import { museTest } from '../muse-fixtures'

museTest('shows an advancing native generation counter and retains the answer', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
