import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { configurationFiles: [join(environment.HOME!, '.omp', 'profiles', environment.OMP_PROFILE!, 'agent', 'models.yml')], configurationMarkers: ['LEAPMUX_E2E_MODEL_API_KEY'], privateDirectories: [join(leapmuxServer.agentEnv.HOME!, '.omp', 'profiles', leapmuxServer.agentEnv.OMP_PROFILE!, 'agent')!], expectedCredential: 'LEAPMUX_E2E_MODEL_API_KEY' })
})
