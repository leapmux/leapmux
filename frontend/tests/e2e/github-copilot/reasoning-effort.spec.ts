import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, waitForSettingsIdle } from '../helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('keeps Plan mode and low effort after a turn and reload', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  const mode = `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`
  await chooseSettingsOption(page, 'effort-low')
  await chooseSettingsOption(page, mode)
  await waitForSettingsIdle(page)
  await expectSettingsOptionChosen(page, mode)
  await expectSettingsChip(page, 'Low')
  const request = await sendNativeAnswer(context, 'Reply once after the settings change.', 'Settings applied.')
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({
    reasoning_effort: 'low',
    tools: expect.arrayContaining([expect.objectContaining({ function: expect.objectContaining({ name: 'exit_plan_mode' }) })]),
  })
  await exerciseRestoredNativeOption(context, {
    groupId: 'effort',
    value: 'low',
    nativeProof(restored) {
      expect(restored.protocol).toBe('openai-chat-completions')
      expect(restored.body).toMatchObject({ reasoning_effort: 'low' })
      expect(restored.body).toMatchObject({ tools: expect.arrayContaining([expect.objectContaining({ function: expect.objectContaining({ name: 'exit_plan_mode' }) })]) })
    },
  })
  await expectSettingsOptionChosen(page, mode)
  await expectSettingsChip(page, 'Low')
})
