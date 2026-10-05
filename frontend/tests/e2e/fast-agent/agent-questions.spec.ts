import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { fastAgentHumanInputToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

fastAgentTest('returns the native human-input callback refusal without a question form', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await expectNoNativeControl(context, { testId: 'elicitation-form', relatedControl: async () => {
    const start = (await modelScript.status()).stepCount
    const callId = 'fast-native-human-input'
    await modelScript.queue(
      { toolCalls: [fastAgentHumanInputToolCall(callId, 'Choose the required color.', ['Blue', 'Red'])] },
      nativeTextStep(context, 'The native question refusal reached this answer.'),
    )
    await sendMessage(page, modelScript.prompt('Run the native human-input tool once.'))
    const status = await modelScript.waitForSteps(start + 2)
    // Fast Agent registers its terminal form as the elicitation callback of each agent.
    // Under ACP, stdin carries the protocol and is not a terminal. The form ends with its default cancel action.
    expect(nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), callId)).toContain('The Human cancelled the input request')
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  } })
})
