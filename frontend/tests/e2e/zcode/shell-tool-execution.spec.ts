import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

zcodeTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  // ZCode begins the result of a failed command with an `Exit code N` line. The row header states the code, and the
  // body draws only the output. No other ZCode test reloads a `Bash` row: its output-path test uses the workflow route.
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native), absentRowText: ['Exit code'], reload: true })
})
