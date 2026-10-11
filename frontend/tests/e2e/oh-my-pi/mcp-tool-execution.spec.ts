import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('executes a disposable MCP echo tool', async ({ native }) => {
  await exerciseMcpEcho(native, 'ohmypi')
})
