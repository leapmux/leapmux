import { expect, FAST_AGENT_AGENT, fastAgentTest } from '../fastagent-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { ARITHMETIC_PROMPT, bandRows, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

fastAgentTest.describe('Fast Agent thinking and context usage', () => {
  const REASONING = 'I add the two numbers column by column.'

  fastAgentTest('draws the reasoning in a thought band', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT, { model: MOCK_MODELS.zai })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.queue({ reasoning: REASONING, text: '6912' })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ model: MOCK_MODELS.zai })
    await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
    // The reasoning stays out of the answer text.
    await expect(bandRows(page, 'text').filter({ hasText: REASONING })).toHaveCount(0)
  })
})
