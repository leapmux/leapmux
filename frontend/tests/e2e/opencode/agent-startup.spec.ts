import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'
import { nativeLaunch } from './scenarios'

opencodeTest.describe('opencode agent startup', () => {
  for (const failed of [false, true]) {
    opencodeTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native }) => {
      await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed })
    })
  }
})
