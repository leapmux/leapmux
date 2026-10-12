import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

deepseekHarnessTest('counts advancing native model chunks and retains their completed output', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
