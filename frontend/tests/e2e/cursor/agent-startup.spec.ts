import { cursorTest } from '../cursor-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

cursorTest.describe('cursor agent startup', () => {
  for (const failed of [false, true]) {
    cursorTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native }) => {
      await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed })
    })
  }
})
