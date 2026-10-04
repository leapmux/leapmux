import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('proves calculated native stdout and failed stderr reach the next turn', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await exerciseShellToolExecution(context)
})
