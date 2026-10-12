import { geminiTest } from '../gemini-fixtures'
import { exerciseGeminiShellToolExecution } from './shellScenarios'

geminiTest('runs native shell output and failed commands', async ({ native }) => {
  await exerciseGeminiShellToolExecution(native)
})
