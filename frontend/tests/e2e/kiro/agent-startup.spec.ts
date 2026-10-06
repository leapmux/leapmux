import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { newProviderWorkingDir } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { nativeLaunch } from './scenarios'

kiroTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: newProviderWorkingDir(KIRO_AGENT) })
})

kiroTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: newProviderWorkingDir(KIRO_AGENT), failed: true })
})
