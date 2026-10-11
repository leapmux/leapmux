import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { piTest } from '../pi-fixtures'

piTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  // Pi ends the output of a failed command with `Command exited with code N`. The row header states the code, and the
  // body draws only the output. No other Pi test reloads a `bash` row: its output-path tests use the codemode route.
  await exerciseShellToolExecution(native, { absentRowText: ['Command exited with code'], reload: true })
})
