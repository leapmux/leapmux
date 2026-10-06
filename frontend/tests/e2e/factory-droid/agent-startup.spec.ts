import { droidTest } from '../droid-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

droidTest('delivers input queued while the actual native process starts', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: false })
})

droidTest('keeps queued input when the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
