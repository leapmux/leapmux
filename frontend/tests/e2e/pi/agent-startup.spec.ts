import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'
import { nativeLaunch } from './scenarios'

piTest.describe('pi agent startup', () => {
  for (const failed of [false, true]) {
    piTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native }) => {
      await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed })
    })
  }
})
