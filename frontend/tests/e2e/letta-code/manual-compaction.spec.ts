// The native compaction command is not integrated.
// See https://github.com/leapmux/leapmux/issues/500.
import { lettaTest } from '../letta-fixtures'
import { exerciseOrdinaryCompactText } from './compactionScenarios'

lettaTest.describe('native manual compaction', () => {
  lettaTest('passes compact text to the model on its App Server path', async ({ native }) => {
    await exerciseOrdinaryCompactText(native)
  })
})
