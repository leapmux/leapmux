import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { lettaTest } from '../letta-fixtures'

lettaTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  // Letta Code begins the result of a failed command with `Exit code: N`. The row header states the code, and the body
  // draws only the output.
  await exerciseShellToolExecution(native, { absentRowText: ['Exit code:'] })
})
