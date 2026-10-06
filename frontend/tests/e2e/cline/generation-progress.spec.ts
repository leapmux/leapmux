import { clineTest } from '../cline-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

clineTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
