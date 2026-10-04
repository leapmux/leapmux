import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { configurationFiles: [join(environment.KIRO_HOME!, 'settings', 'cli.json')], inlineConfiguration: [JSON.stringify({ apiKey: environment.KIRO_API_KEY })], privateDirectories: [leapmuxServer.agentEnv.KIRO_HOME!], expectedCredential: environment.KIRO_API_KEY! })
})
