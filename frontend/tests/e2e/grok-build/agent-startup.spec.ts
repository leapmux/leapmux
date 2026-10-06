import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { newProviderWorkingDir } from '../helpers/workspace'
import { nativeLaunch } from './scenarios'

grokTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: newProviderWorkingDir(GROK_AGENT) })
})

grokTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: newProviderWorkingDir(GROK_AGENT), failed: true })
})
