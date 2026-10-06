import { join } from 'node:path'
import { KIRO_E2E_API_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { kiroTest } from '../kiro-fixtures'

kiroTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  // Kiro reads its key from the environment. The expected key is the suite constant, so the proof compares the
  // private environment with it.
  await exerciseCredentialIsolation(native, { configurationFiles: [join(environment.KIRO_HOME!, 'settings', 'cli.json')], inlineConfiguration: [environment.KIRO_API_KEY!], privateDirectories: [environment.KIRO_HOME!], expectedCredential: KIRO_E2E_API_KEY })
})
