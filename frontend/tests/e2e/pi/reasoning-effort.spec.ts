import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest('keeps the low effort after a turn and reload', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await chooseSettingsOption(page, `model-${MOCK_MODELS.zai}`)
  await chooseSettingsOption(page, 'effort-low')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Low')
  const request = await sendNativeAnswer(context, 'Reply once after the settings change.', 'Settings applied.')
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'low' })
  await exerciseRestoredNativeOption(context, {
    groupId: 'effort',
    value: 'low',
    nativeProof(restored) {
      expect(restored.protocol).toBe('openai-chat-completions')
      expect(restored.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'low' })
    },
  })
  await expectSettingsChip(page, 'Low')
})

// Pi can move the thinking level when the model changes, and the update reads the level back after the switch.
piTest('keeps the chosen effort after a model switch and a reload', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'low' },
    model: MOCK_MODELS.zai,
    nativeProof(request) {
      expect(request.protocol).toBe('openai-chat-completions')
      expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'low' })
    },
  })
})
