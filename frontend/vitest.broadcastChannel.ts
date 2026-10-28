import { expect } from 'vitest'
import { installBroadcastChannelIsolation } from './src/test-support/broadcastChannelIsolation'

// Storage constructs its channel during import, before the main setup's hooks.
installBroadcastChannelIsolation(expect.getState().testPath)
