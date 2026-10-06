import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'

gooseTest('model: keeps the high effort after a turn and reload', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await chooseSettingsOption(page, `model-${MOCK_MODELS.gooseReasoning}`)
  await expectSettingsChip(page, MOCK_MODELS.gooseReasoning)
  await chooseSettingsOption(page, 'thinking_effort-high')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'High')
  const request = await sendNativeAnswer(context, 'Reply once after the settings change.', 'Settings applied.')
  expect(request.protocol).toBe('openai-responses')
  expect(request.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning, reasoning: { effort: 'high' } })
  await exerciseRestoredNativeOption(context, {
    groupId: 'model',
    value: MOCK_MODELS.gooseReasoning,
    nativeProof(restored) {
      expect(restored.protocol).toBe('openai-responses')
      expect(restored.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning, reasoning: { effort: 'high' } })
    },
  })
  await expectSettingsChip(page, MOCK_MODELS.gooseReasoning)
  await expectSettingsChip(page, 'High')
})
