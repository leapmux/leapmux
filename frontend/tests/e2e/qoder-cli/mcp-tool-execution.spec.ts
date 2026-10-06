import { qoderTest } from '../qoder-fixtures'
import { exerciseQoderMcpForm } from './mcpScenarios'

qoderTest('returns the actual local MCP tool result to the native model', async ({ native }) => {
  await exerciseQoderMcpForm(native)
})
