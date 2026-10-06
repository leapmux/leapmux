import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { kiloTest } from '../kilo-fixtures'

kiloTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'kilo')
})
