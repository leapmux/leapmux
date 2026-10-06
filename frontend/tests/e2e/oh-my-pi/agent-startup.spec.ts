import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { nativeLaunch } from './scenarios'

ohMyPiTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
})

ohMyPiTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
