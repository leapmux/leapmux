import { clineTest, createClineWorkingDir } from '../cline-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeLaunch } from './scenarios'

clineTest('delivers input through a controlled native startup', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: createClineWorkingDir() })
})

clineTest('retains input after the actual native launch fails', async ({ native }) => {
  await exerciseAgentStartup(native, { launch: nativeLaunch(native), workingDir: createClineWorkingDir(), failed: true })
})
