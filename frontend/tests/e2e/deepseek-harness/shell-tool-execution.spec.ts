import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

deepseekHarnessTest('runs native commands and preserves their output and nonzero exit codes', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
