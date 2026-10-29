import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseProviderAgentButtons } from '../helpers/nativeAgentOpen'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

deepseekHarnessTest('delivers queued startup input and keeps it after a native startup failure', async ({ native }) => {
  await exerciseAgentStartup(native, {
    launch: nativeLaunch(native),
  })
  await exerciseAgentStartup(native, {
    launch: nativeLaunch(native),
    failed: true,
  })
})

deepseekHarnessTest('opens another provider agent from the menu and button', async ({ native }) => {
  await exerciseProviderAgentButtons(native)
})
