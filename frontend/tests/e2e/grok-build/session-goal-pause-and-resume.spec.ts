import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseNativeGoalPauseAndResume } from '../helpers/nativeGoalLifecycle'
import { messageBubbles } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('records the native user pause and resumes new native goal model work', async ({ authenticatedGrokWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  await exerciseNativeGoalPauseAndResume(context, { pausedProof: async () => {
    await expect(messageBubbles(page).filter({ hasText: 'Goal paused. Use /goal resume to continue.' }).first()).toBeVisible()
  } })
})
