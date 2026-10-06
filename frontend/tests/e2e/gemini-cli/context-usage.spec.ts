import { geminiTest } from '../gemini-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

geminiTest('uses the last native request counts through the context surface', async ({ native }) => {
  await exerciseContextUsage(native)
})
