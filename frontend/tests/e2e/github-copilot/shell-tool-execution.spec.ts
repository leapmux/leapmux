import { copilotTest } from '../copilot-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bypassToolRequests } from './scenarios'

copilotTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native) })
})
