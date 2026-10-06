import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { reasonixTest } from '../reasonix-fixtures'
import { nativeLaunch } from './scenarios'

reasonixTest.describe('reasonix agent startup', () => {
  for (const failed of [false, true]) {
    reasonixTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native }) => {
      await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed })
    })
  }
})
