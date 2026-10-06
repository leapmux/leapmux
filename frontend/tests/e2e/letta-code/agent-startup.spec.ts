import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'
import { nativeLaunch } from './scenarios'

lettaTest('delivers input queued while the actual native process starts', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: false })
})

lettaTest('keeps queued input when the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
