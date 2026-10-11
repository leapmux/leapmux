import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'opencode')
})
