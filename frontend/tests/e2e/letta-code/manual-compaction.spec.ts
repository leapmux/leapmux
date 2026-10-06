import { lettaTest } from '../letta-fixtures'
import { exerciseOrdinaryCompactText } from './compactionScenarios'

lettaTest.describe('native manual compaction', () => {
  lettaTest('passes compact text to the model on its App Server path', async ({ native }) => {
    await exerciseOrdinaryCompactText(native)
  })
})
