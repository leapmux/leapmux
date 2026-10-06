import { AgentGoalAction, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { goalAction } from '../helpers/subagentRegistry'
import { waitForSettingsHydrated } from '../helpers/ui'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { expect, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest.describe('junie unsupported controls', () => {
  junieTest('does not offer Set for the session goal', async ({ authenticatedJunieWorkspace, page, leapmuxServer }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first().getAttribute('data-tab-id')
    expect(agentId).not.toBeNull()
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    let actions: AgentGoalAction[] = []
    await expect.poll(async () => {
      const response = await channel.callWorker(
        leapmuxServer.workerId,
        'ListAgentMessages',
        ListAgentMessagesRequestSchema,
        ListAgentMessagesResponseSchema,
        { agentId: agentId ?? '', limit: 1 },
      )
      actions = response.goalSupportedActions
      return actions
    }).toContain(AgentGoalAction.CLEAR)
    expect(actions).not.toContain(AgentGoalAction.SET)
    await expect(goalAction(page, 'set')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Set a goal' })).toHaveCount(0)
  })
})

junieTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.SET], relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete before the native goal refusal.', 'The native goal probe completed.')
  } })
})
