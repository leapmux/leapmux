import { gooseTest } from '../goose-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bypassToolRequests } from './scenarios'

gooseTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native) })
})
