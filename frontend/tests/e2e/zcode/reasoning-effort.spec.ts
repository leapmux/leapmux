import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('keeps the low effort after a turn and reload', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await chooseSettingsOption(page, 'effort-low')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Low')
  const request = await sendNativeAnswer(context, 'Reply once after the settings change.', 'Settings applied.')
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ reasoning_effort: 'low' })
  await exerciseRestoredNativeOption(context, {
    groupId: 'effort',
    value: 'low',
    nativeProof(restored) {
      expect(restored.protocol).toBe('openai-chat-completions')
      expect(restored.body).toMatchObject({ reasoning_effort: 'low' })
    },
  })
  await expectSettingsChip(page, 'Low')
})

// Both mock models offer low, medium, and high. ZCode starts the new model at its own default level.
zcodeTest('keeps the chosen effort after a model switch and a reload', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'low' },
    model: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`,
    nativeProof(request) {
      expect(request.protocol).toBe('openai-chat-completions')
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'low' })
    },
  })
})
