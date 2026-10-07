import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

commandCodeTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  // Command Code begins the result of a command that exits nonzero with `Exit code: N`. The row header states the
  // code, and the body draws only the output.
  await exerciseShellToolExecution(native, { absentRowText: ['Exit code:'] })
})
