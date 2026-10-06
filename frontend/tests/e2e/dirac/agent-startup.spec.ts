import { diracTest } from '../dirac-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

diracTest('delivers input queued while the actual native process starts', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: false })
})

diracTest('keeps queued input when the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
