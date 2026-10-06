import { claudeTest } from '../claude-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

claudeTest('closes the native process and its real tool through the UI', async ({ native }) => {
  await exerciseCloseAgent(native)
})
