import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { qwenTest } from '../qwen-fixtures'

qwenTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'qwen')
})
