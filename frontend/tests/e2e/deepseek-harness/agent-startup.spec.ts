import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

deepseekHarnessTest('delivers queued startup input and keeps it after a native startup failure', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
