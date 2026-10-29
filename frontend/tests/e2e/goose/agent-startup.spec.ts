import { gooseTest } from '../goose-fixtures'
import { exerciseProviderAgentButtons } from '../helpers/nativeAgentOpen'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

gooseTest.describe('goose agent startup', () => {
  for (const failed of [false, true]) {
    gooseTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ native }) => {
      await exerciseAgentStartup(native, {
        launch: nativeLaunch(native),
        failed,
      })
    })
  }
})

gooseTest('opens another provider agent from the menu and button', async ({ native }) => {
  await exerciseProviderAgentButtons(native)
})
