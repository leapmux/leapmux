import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseMcpEcho } from '../helpers/mcpExecution'

codewhaleTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'codewhale')
})
