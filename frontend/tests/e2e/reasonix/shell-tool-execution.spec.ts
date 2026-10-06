import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

reasonixTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native) })
})
