import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseProviderAgentButtons } from '../helpers/nativeAgentOpen'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

fastAgentTest('delivers input queued while the actual native process starts', async ({ native }) => {
  await exerciseAgentStartup(native, {
    launch: nativeLaunch(native),
    failed: false,
  })
})

fastAgentTest('keeps queued input when the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, {
    launch: nativeLaunch(native),
    failed: true,
  })
})

fastAgentTest('opens another provider agent from the menu and button', async ({ native }) => {
  await exerciseProviderAgentButtons(native)
})
