import { geminiTest } from '../gemini-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

geminiTest('closes the agent tab and waits for its owned native process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
