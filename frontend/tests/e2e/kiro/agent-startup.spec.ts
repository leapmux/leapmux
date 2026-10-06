import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { createKiroWorkingDir, kiroTest } from '../kiro-fixtures'
import { nativeLaunch } from './scenarios'

kiroTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: createKiroWorkingDir() })
})

kiroTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: createKiroWorkingDir(), failed: true })
})
