import { clineTest } from '../cline-fixtures'
import { exerciseMcpEcho } from '../helpers/mcpExecution'

clineTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'cline')
})
