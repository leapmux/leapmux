import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

codebuddyTest('delivers input queued while the actual native process starts', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: false })
})

codebuddyTest('keeps queued input when the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
