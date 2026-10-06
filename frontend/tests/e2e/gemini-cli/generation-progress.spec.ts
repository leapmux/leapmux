import { geminiTest } from '../gemini-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

geminiTest('updates the native generation counter before a held turn completes', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
