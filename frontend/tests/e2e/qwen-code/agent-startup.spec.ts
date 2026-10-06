import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { qwenTest } from '../qwen-fixtures'
import { nativeLaunch } from './scenarios'

qwenTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
})

qwenTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
