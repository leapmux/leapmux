import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('the mode menu lists the four advertised modes', async ({ authenticatedCodebuddyWorkspace, page }) => {
    void authenticatedCodebuddyWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(CODEBUDDY_MODE))
    // The fixture opens the agent in Bypass Permissions.
    await expectSettingsOptionChosen(page, `permissionMode-${CODEBUDDY_MODE.BypassPermissions}`)
  })

  codebuddyTest('keeps selected Plan mode for the next native turn', async ({ native }) => {
    const { page, modelScript } = native
    const baseline = 'The first mode probe answered.'
    await sendNativeAnswer(native, 'Reply before I change mode.', baseline)

    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Plan}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')
    const planStep = await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.CODEBUDDY, 'exit-selected-plan', '# Probe plan')] })
    await modelScript.fallback({ text: 'The mode probe ended.' })
    await sendMessage(page, modelScript.prompt('Present the plan for review after the mode change.'))
    const next = await modelScript.requestAt(planStep)
    expect(next.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(next.body)).toContain(baseline)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await answerPlanReview(page, 'reject')
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Plan')
  })
})
