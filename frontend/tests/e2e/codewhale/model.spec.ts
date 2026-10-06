import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'

codewhaleTest('sends the selected model into native requests before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await exerciseNativeOption(context, { groupId: 'model', value: CODEWHALE_VISION_MODEL_ID, nativeProof: (request) => {
    expect(request.body).toHaveProperty('model', CODEWHALE_VISION_MODEL_ID)
  } })
})
