import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { kimiTest } from '../kimi-fixtures'

kimiTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'kimi')
})
