import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

deepseekHarnessTest('runs native commands and preserves their output and nonzero exit codes', async ({ native }) => {
  // The harness ends the result of a command that exits nonzero with `[exit code: N]`. The row header states the code,
  // and the body draws only the output.
  await exerciseShellToolExecution(native, { absentRowText: ['[exit code:'] })
})
