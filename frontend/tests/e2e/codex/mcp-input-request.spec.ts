import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { MCP_FORM_SERVER_NAME } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { answerControl, waitForControlBanner } from '../helpers/ui'

codexTest.describe('Codex MCP input form', () => {
  codexTest('sends zero and false form values to the native MCP server', async ({ native }) => {
    // The Codex configuration of the run starts the form server. Codex asks before it runs an MCP tool.
    await exerciseMcpProbeFormRoundTrip(native, {
      callId: 'codex-mcp-form',
      reloadBeforeSubmit: false,
      approveTool: async () => {
        const permission = await waitForControlBanner(native.page)
        await expect(permission).toContainText(`Allow the ${MCP_FORM_SERVER_NAME} MCP server to run tool "ask"?`)
        await answerControl(native.page, 'allow')
      },
    })
  })
})
