import { geminiTest } from '../gemini-fixtures'
import { exerciseProviderAgentButtons } from '../helpers/nativeAgentOpen'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

for (const failed of [false, true]) {
  geminiTest(failed ? 'retains queued input after a real native launch failure' : 'delivers input through a controlled native launch', async ({ native }) => {
    await exerciseAgentStartup(native, {
      launch: nativeLaunch(native),
      failed,
    })
  })
}

geminiTest('opens another provider agent from the menu and button', async ({ native }) => {
  await exerciseProviderAgentButtons(native)
})
