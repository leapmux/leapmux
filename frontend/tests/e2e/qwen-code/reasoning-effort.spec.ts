import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { QWEN_ALT_MODEL_ID, QWEN_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('sends the chosen effort into native turns before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await exerciseNativeOption(context, { groupId: 'reasoning_effort', value: 'low', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// Both models offer low, medium, and high. The server keeps the tier across the switch, so the kept
// value must reach the next native request of the new model.
qwenTest('keeps the chosen effort after a model switch and a reload', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'reasoning_effort', value: 'low' },
    model: QWEN_ALT_MODEL_ID,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: QWEN_ALT_MODEL_WIRE_ID, reasoning_effort: 'low' })
    },
  })
})
