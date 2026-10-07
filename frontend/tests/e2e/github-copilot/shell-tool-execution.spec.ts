import { copilotTest } from '../copilot-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bypassToolRequests } from './scenarios'

copilotTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  // GitHub Copilot ends each result with `<shellId: N completed with exit code N>`. The row header states a nonzero
  // code, and the body draws only the output.
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native), absentRowText: ['<shellId:', 'completed with exit code'] })
})
