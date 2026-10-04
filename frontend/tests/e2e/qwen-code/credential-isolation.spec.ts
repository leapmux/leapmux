import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { configurationFiles: [join(environment.QWEN_HOME!, 'settings.json')], privateDirectories: [leapmuxServer.agentEnv.QWEN_HOME!], expectedCredential: environment.LEAPMUX_E2E_MODEL_API_KEY! })
})
