import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { KIRO_MOCK_MODELS } from '../helpers/kiroSurface'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeLastStepBody } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectNoSettingsChip, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kiroTest, openKiroAgent } from '../kiro-fixtures'

const [EFFORT_MODEL, PLAIN_MODEL] = KIRO_MOCK_MODELS

/** The model a Kiro turn states, in its current message. */
function requestModel(body: Record<string, unknown>): unknown {
  const state = body.conversationState as { currentMessage?: { userInputMessage?: { modelId?: unknown } } } | undefined
  return state?.currentMessage?.userInputMessage?.modelId
}

kiroTest.describe('Kiro settings', () => {
  kiroTest('switches the effort, the mode and the model for the next prompt, and keeps them after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')
    // The open request stated the effort, which differs from the model's own default.
    await expectSettingsChip(page, 'Medium')

    await chooseSettingsOption(page, 'effortLevel-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue({ text: 'SETTINGS_APPLIED' })
    await sendMessage(page, modelScript.prompt('Describe the plan in one word.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const planned = nativeLastStepBody((await modelScript.status()).requests)
    expect(planned.agentMode).toBe('plan')
    expect(planned.additionalModelRequestFields).toEqual({ output_config: { effort: 'high' } })
    expect(requestModel(planned)).toBe(EFFORT_MODEL!.modelId)

    // A model with no effort axis drops the axis, and its turns state no effort.
    await chooseSettingsOption(page, `model-${PLAIN_MODEL!.modelId}`)
    await waitForSettingsIdle(page)
    await expectNoSettingsChip(page, 'High')
    await modelScript.queue({ text: 'MODEL_SWITCHED' })
    await sendMessage(page, modelScript.prompt('Answer with the other model.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const switched = nativeLastStepBody((await modelScript.status()).requests)
    expect(requestModel(switched)).toBe(PLAIN_MODEL!.modelId)
    expect(switched).not.toHaveProperty('additionalModelRequestFields')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsOptionChosen(page, `model-${PLAIN_MODEL!.modelId}`)
    await expectNoSettingsChip(page, 'High')
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.KIRO }, 'Reply after restoring the selected native settings.', 'The restored Kiro settings answered.')
    expect(restored.body).toHaveProperty('agentMode', 'plan')
    expect(restored.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', PLAIN_MODEL!.modelId)
    expect(restored.body).not.toHaveProperty('additionalModelRequestFields')
  })
})
