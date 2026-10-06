import { CLINE_AGENT, clineTest } from '../cline-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { newProviderWorkingDir } from '../helpers/workspace'
import { nativeLaunch } from './scenarios'

clineTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: newProviderWorkingDir(CLINE_AGENT) })
})

clineTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: newProviderWorkingDir(CLINE_AGENT), failed: true })
})
