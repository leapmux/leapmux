import { createGrokWorkingDir, grokTest } from '../grok-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

grokTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: createGrokWorkingDir() })
})

grokTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: createGrokWorkingDir(), failed: true })
})
