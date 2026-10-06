import { AgentGoalAction, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { goalAction, nativeGoalProbeTurn } from '../helpers/goalsAndTodos'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { waitForSettingsHydrated } from '../helpers/ui'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { expect, junieTest } from '../junie-fixtures'

junieTest.describe('junie unsupported controls', () => {
  junieTest('does not offer Set for the session goal', async ({ native }) => {
    const { page, leapmuxServer } = native
    await waitForSettingsHydrated(page)
    const agentId = await selectedAgentTabId(page)
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const actions = await retryUntilPass(async () => {
      const response = await channel.callWorker(
        leapmuxServer.workerId,
        'ListAgentMessages',
        ListAgentMessagesRequestSchema,
        ListAgentMessagesResponseSchema,
        { agentId, limit: 1 },
      )
      expect(response.goalSupportedActions, 'the Worker states the goal actions of the agent').toContain(AgentGoalAction.CLEAR)
      return response.goalSupportedActions
    })
    expect(actions).not.toContain(AgentGoalAction.SET)
    await expect(goalAction(page, 'set')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Set a goal' })).toHaveCount(0)
  })
})

junieTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.SET], relatedProof: async () => {
    await nativeGoalProbeTurn(native)
  } })
})
