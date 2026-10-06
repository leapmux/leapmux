import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { kiloTest } from '../kilo-fixtures'
import { nativeLaunch } from './scenarios'

kiloTest.describe('kilo agent startup', () => {
  for (const failed of [false, true]) {
    kiloTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native }) => {
      await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed })
    })
  }
})
