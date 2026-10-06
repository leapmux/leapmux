import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

reasonixTest('closes the native agent and its actual owned process tree', async ({ native }) => {
  await exerciseCloseAgent(native, { prepare: () => bypassToolRequests(native) })
})
