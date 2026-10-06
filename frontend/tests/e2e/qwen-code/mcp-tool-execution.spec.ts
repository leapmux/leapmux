import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { qwenTest } from '../qwen-fixtures'

qwenTest('executes a disposable MCP echo tool', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
  void authenticatedQwenWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.QWEN_CODE, 'qwen')
})
