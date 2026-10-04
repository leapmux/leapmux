import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseNativeOption } from '../helpers/nativeSettings'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('sends the chosen effort into native turns before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  await exerciseNativeOption(context, { groupId: 'reasoning_effort', value: 'high', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'high')
  } })
})
