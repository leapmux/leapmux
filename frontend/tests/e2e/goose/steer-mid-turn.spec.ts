import { gooseTest } from '../goose-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { bypassToolRequests } from './scenarios'

// Goose advertises the request in no capability list; LeapMux sends it anyway
// (user decision 2026-10-25), and a Goose that drops it answers -32601, which
// falls the input back to the queue.
gooseTest('steers a running turn through the native unstable steer request', async ({ native }) => {
  await exerciseSteerAfterTool(native, { prepare: () => bypassToolRequests(native), expectDisplayedOutput: false })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/519.
