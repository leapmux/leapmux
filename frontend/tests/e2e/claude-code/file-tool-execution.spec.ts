import { claudeTest } from '../claude-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'

claudeTest('reads, edits, and creates actual files through native tools', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
