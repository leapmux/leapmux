import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseCompactPreviewRefusal, exerciseCompactRefusal } from './compactionScenarios'

fastAgentTest.describe('native manual compaction', () => {
  fastAgentTest('refuses the compact command on its ACP path', async ({ native }) => {
    await exerciseCompactPreviewRefusal(native)
  })

  fastAgentTest('refuses the exact compact command without a completed boundary', async ({ native }) => {
    await exerciseCompactRefusal(native)
  })
})
