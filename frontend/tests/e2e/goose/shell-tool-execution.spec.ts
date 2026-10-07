import { gooseTest } from '../goose-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bypassToolRequests } from './scenarios'

gooseTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  // Goose opens each result with an `exit code: N` block. The row header states a nonzero code, and the body draws
  // only the output.
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native), absentRowText: ['exit code:'] })
})
