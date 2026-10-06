import { exerciseTokenProgress } from '../helpers/generationProgress'
import { kiroTest } from '../kiro-fixtures'

kiroTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
