import { join } from 'node:path'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const agentDirectory = join(environment.HOME!, '.omp', 'profiles', environment.OMP_PROFILE!, 'agent')
  // Oh My Pi reads `apiKey` as the identifier of an environment variable first. So the file must identify the
  // variable and must not hold the key. The accepted mock credential of the turn request proves that Oh My Pi read
  // the key from the variable.
  await exerciseCredentialIsolation(native, {
    configurationFiles: [join(agentDirectory, 'models.yml')],
    privateDirectories: [agentDirectory],
    absentFromConfiguration: [MODEL_KEY],
    configurationMarkers: ['"apiKey": "LEAPMUX_E2E_MODEL_API_KEY"'],
  })
})
