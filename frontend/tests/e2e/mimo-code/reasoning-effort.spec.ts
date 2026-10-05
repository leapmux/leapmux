import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('sends the chosen effort into native turns before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseNativeOption(context, { groupId: 'effort', value: 'low', prepare: async () => {
    await chooseSettingsOption(page, `model-${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`)
    await waitForSettingsIdle(page)
  }, nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// MiMo keeps the model, the effort, and the mode as LeapMux state, so the next prompt must state both after a switch.
mimoTest('keeps the chosen effort after a model switch and a reload', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'low' },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'low' })
    },
  })
})
