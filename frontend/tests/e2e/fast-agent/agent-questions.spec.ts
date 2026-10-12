import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { fastAgentHumanInputToolCall } from '../helpers/providerToolCalls'
import { expectNoControlBanner, sendMessage, waitForAgentIdle } from '../helpers/ui'

// The turn scripts its steps by hand: the shared tool turn allows every native approval, and this proof requires that
// no control appears at all.
fastAgentTest('returns the native human-input callback refusal without a question form', async ({ native }) => {
  const { page, modelScript } = native
  await expectNoNativeControl(native, { testId: 'elicitation-form', relatedProof: async () => {
    const callId = 'fast-native-human-input'
    const start = await modelScript.queue(
      { toolCalls: [fastAgentHumanInputToolCall(callId, 'Choose the required color.', ['Blue', 'Red'])] },
      nativeTextStep(native, 'The native question refusal reached this answer.'),
    )
    await sendMessage(page, modelScript.prompt('Run the native human-input tool once.'))
    await modelScript.waitForSteps(start + 2)
    // Fast Agent registers its terminal form as the elicitation callback of each agent.
    // Under ACP, stdin carries the protocol and is not a terminal. The form ends with its default cancel action.
    expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain('The Human cancelled the input request')
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
  } })
})
