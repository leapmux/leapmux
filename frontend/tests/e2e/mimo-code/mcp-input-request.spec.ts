import { expect } from '@playwright/test'
import { invokeNativeMcpTool, nativeMcpAnswer } from '../helpers/mcpExecution'
import { MCP_FORM_SERVER_NAME } from '../helpers/mcpFormServer'
import { MIMO_INPUT_FORM_SERVER_NAME } from '../helpers/mimoEnvironment'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlButton, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code MCP confirmation', () => {
  for (const choice of [
    { decision: 'allow', result: 'MCP_CONFIRM_ACCEPTED', title: 'accepts an empty native MCP form after reload' },
    { decision: 'deny', result: 'MCP_CONFIRM_DECLINED', title: 'declines an empty native MCP form after reload' },
    { decision: 'cancel', result: 'MCP_CONFIRM_CANCELLED', title: 'cancels an empty native MCP form after reload' },
  ] as const) {
    mimoTest(choice.title, async ({ native }) => {
      const { page, modelScript } = native
      const callId = `mimo-confirm-${choice.decision}`
      const answer = nativeMcpAnswer(callId)
      const start = await modelScript.queue({
        toolCalls: [
          mcpToolCall(native.provider, callId, { server: MCP_FORM_SERVER_NAME, tool: 'ask', input: {} }),
        ],
      }, { text: answer })
      await sendMessage(page, modelScript.prompt('Call the native confirmation probe once.'))
      await modelScript.waitForSteps(start + 1)
      await expect(await waitForControlBanner(page)).toContainText('Allow the probe action?')
      await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
      await page.reload()
      await expect(await waitForControlBanner(page)).toContainText('Allow the probe action?')
      if (choice.decision === 'cancel')
        await page.getByTestId('control-more-actions').filter({ visible: true }).click()
      await controlButton(page, choice.decision).click()
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toBe(choice.result)
      await expect(assistantBubbles(page).filter({ hasText: answer })).toBeVisible()
      await expectNoControlBanner(page)
    })
  }

  // MiMo 0.1.17 supports empty confirmation forms. Its native handler rejects typed fields before it asks a question.
  mimoTest('shows no form when the native client refuses typed MCP input', async ({ native }) => {
    const callId = 'mimo-typed-form'
    await expectNoNativeControl(native, {
      testId: 'elicitation-form',
      additionalTestIds: ['control-banner'],
      relatedProof: async () => {
        const request = await invokeNativeMcpTool(native, {
          server: MIMO_INPUT_FORM_SERVER_NAME,
          tool: 'ask',
          callId,
          input: {},
        })
        expect(nativeToolResult(request, callId)).toBe('FORM_ROUND_TRIP_REFUSED: -32602 MCP error -32602: Only empty confirmation forms are supported by this client.')
      },
    })
    await expect(assistantBubbles(native.page).filter({
      hasText: nativeMcpAnswer(callId),
    })).toBeVisible()
  })
})
