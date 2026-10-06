import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { kimiTest } from '../kimi-fixtures'
import { nativeLaunch } from './scenarios'

kimiTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
})

kimiTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
