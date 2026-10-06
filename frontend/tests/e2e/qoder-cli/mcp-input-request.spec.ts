import { qoderTest } from '../qoder-fixtures'
import { exerciseQoderMcpForm } from './mcpScenarios'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI MCP input form', () => {
  qoderTest('returns zero and false values to the native MCP tool', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    await exerciseQoderMcpForm(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId }))
  })
})
