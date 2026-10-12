import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { museTest } from '../muse-fixtures'
import { nativeLaunch } from './scenarios'

museTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
})

museTest('retains input after an actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
