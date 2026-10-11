import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { mimoTest } from '../mimo-fixtures'

mimoTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'mimo')
})
