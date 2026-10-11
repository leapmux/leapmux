import { exerciseTokenProgress } from '../helpers/generationProgress'
import { kimiTest } from '../kimi-fixtures'

kimiTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
