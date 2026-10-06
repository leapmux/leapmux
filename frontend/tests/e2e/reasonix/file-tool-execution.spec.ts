import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

reasonixTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native, { prepare: () => bypassToolRequests(native) })
})
