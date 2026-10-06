import { copilotTest } from '../copilot-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { bypassToolRequests } from './scenarios'

copilotTest('closes the native agent and its actual owned process tree', async ({ native }) => {
  await exerciseCloseAgent(native, { prepare: () => bypassToolRequests(native) })
})
