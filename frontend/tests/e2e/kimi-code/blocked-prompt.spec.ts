import { kimiTest } from '../kimi-fixtures'
import { exerciseKimiBlockedPrompt } from './scenarios'

kimiTest('shows the hook reason and runs the next prompt', async ({ native }) => {
  await exerciseKimiBlockedPrompt(native)
})
