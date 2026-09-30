import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { withClaudeSubscriberWorker } from './helpers/claudeSubscriberWorker'
import { expectRateLimitWindow, rateLimitWindowLabel } from './helpers/rateLimit'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openAgentInfoCard, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

test.describe('Claude Code rate-limit state', () => {
  test('a subscriber sees the model warning after a reload', async ({ page, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken } = leapmuxServer
    await withClaudeSubscriberWorker(leapmuxServer, async (workerId) => {
      await withTestWorkspace(leapmuxServer, 'claude-rate-limit', async ({ workspaceId }) => {
        const defaults = agentOpenOptions(agentSettings(AgentProvider.CLAUDE_CODE))
        await openAgentViaAPI(
          hubUrl,
          adminToken,
          workerId,
          workspaceId,
          createTestDirectory('claude-subscriber-wd-'),
          {
            agentProvider: AgentProvider.CLAUDE_CODE,
            ...defaults,
            optionValues: { ...defaults.optionValues, permissionMode: 'default' },
          },
        )
        await loginViaToken(page, adminToken)
        await openWorkspace(page, workspaceId)

        const rateLimits = {
          type: 'five_hour',
          status: 'allowed_warning',
          utilization: 0.92,
          resetsAt: Math.floor(Date.now() / 1000) + 3600,
        }
        await modelScript.queue({ text: 'Answered near the limit.', rateLimits })
        await sendMessage(page, modelScript.prompt('Reply once.'))
        await modelScript.waitForSteps()
        await waitForAgentIdle(page)
        await expectRateLimitWindow(page, rateLimits)

        await page.reload()
        await expectRateLimitWindow(page, rateLimits)
      })
    })
  })

  test('the API-key headers do not create subscriber rate-limit state', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    const rateLimits = {
      type: 'five_hour',
      status: 'allowed_warning',
      utilization: 0.92,
      resetsAt: Math.floor(Date.now() / 1000) + 3600,
    }
    await modelScript.queue({ text: 'Answered with an API key.', rateLimits })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const popover = await openAgentInfoCard(page)
    await expect(popover).not.toContainText(rateLimitWindowLabel(rateLimits.type))
  })
})
