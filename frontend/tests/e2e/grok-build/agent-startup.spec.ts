import { grokTest } from '../grok-fixtures'
import { exerciseProviderAgentButtons } from '../helpers/nativeAgentOpen'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

grokTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, {
    launch: nativeLaunch(native),
  })
})

grokTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, {
    launch: nativeLaunch(native),
    failed: true,
  })
})

grokTest('opens another provider agent from the menu and button', async ({ native }) => {
  await exerciseProviderAgentButtons(native)
})
