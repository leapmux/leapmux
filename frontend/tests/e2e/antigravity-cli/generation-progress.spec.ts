import { antigravityTest } from '../antigravity-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

antigravityTest('updates the native generation counter before a held turn completes', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
