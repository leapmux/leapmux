import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { mimoTest } from '../mimo-fixtures'
import { nativeLaunch } from './scenarios'

mimoTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native) })
})

mimoTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), failed: true })
})
