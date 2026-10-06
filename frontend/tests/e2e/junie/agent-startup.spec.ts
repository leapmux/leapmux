import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'
import { nativeLaunch } from './scenarios'

junieTest('delivers input queued while the actual native process starts', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: false })
})

junieTest('keeps queued input when the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
