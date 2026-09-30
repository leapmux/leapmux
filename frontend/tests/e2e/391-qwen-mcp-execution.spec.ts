import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from './helpers/mcpExecution'
import { QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('executes a disposable MCP echo tool', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
  void authenticatedQwenWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.QWEN_CODE, 'qwen')
})
