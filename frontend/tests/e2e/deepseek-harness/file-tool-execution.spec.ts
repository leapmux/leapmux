import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'

deepseekHarnessTest('reads and changes actual file bytes through native tools', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
