import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseContextCompactionWithoutNotice } from './compactionScenarios'

codebuddyTest.describe('CodeBuddy Code compaction notice', () => {
  codebuddyTest('replaces old context after a native manual compaction without a notice', async ({ native }) => {
    await exerciseContextCompactionWithoutNotice(native)
  })
})
