import { expect } from '@playwright/test'
import { invokeNativeMcpTool, nativeMcpAnswer } from '../helpers/mcpExecution'
import { MCP_FORM_SERVER_NAME } from '../helpers/mcpFormServer'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { messageBubbles } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code MCP confirmation', () => {
  // MiMo Code 0.1.15 declares no elicitation capability (its MCP client states
  // only `sampling` and an experimental turn-lifecycle capability) and registers
  // no elicitation handler. Its MCP SDK therefore answers the server's
  // `elicitation/create` with JSON-RPC error -32601 "Method not found". It never
  // sends a decline, so the probe server reports a refusal, not
  // MCP_CONFIRM_DECLINED. The agent environment registers the confirmation
  // server under the name of the form server.
  mimoTest('shows no form when the native client refuses MCP confirmation', async ({ native }) => {
    const callId = 'mimo-confirm'
    await expectNoNativeControl(native, {
      testId: 'elicitation-form',
      additionalTestIds: ['control-banner'],
      relatedProof: async () => {
        const request = await invokeNativeMcpTool(native, { server: MCP_FORM_SERVER_NAME, tool: 'ask', callId, input: {} })
        expect(nativeToolResult(request, callId)).toContain('FORM_ROUND_TRIP_REFUSED: -32601 Method not found')
      },
    })
    await expect(messageBubbles(native.page).filter({ hasText: nativeMcpAnswer(callId) }).first()).toBeVisible()
  })
})
