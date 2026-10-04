import { expect } from '@playwright/test'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { qoderTest } from '../qoder-fixtures'
import { exerciseNativeMcpForm } from './mcpScenarios'
import { nativeContext } from './scenarios'

qoderTest('returns the actual local MCP tool result to the native model', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  const request = await exerciseNativeMcpForm(context)
  expect(nativeToolResult(request, 'qoder-mcp-form')).toContain('FORM_ROUND_TRIP_OK')
})
