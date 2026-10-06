import { commandCodeTest } from '../command-code-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

commandCodeTest('delivers queued startup input and retains it after an actual startup failure', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
