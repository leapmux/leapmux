import { claudeTest } from '../claude-fixtures'
import { exerciseTextGoalQueue } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

claudeTest.describe('claude session goal input queue', () => {
  claudeTest('routes session-goal commands through the input queue', async ({
    authenticatedWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedWorkspace
    // The goal commands drive turns this test does not count.
    await modelScript.fallback({ text: 'Understood.' })
    // Claude starts lazily. Its startup frame advertises /goal after this turn.
    await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await exerciseTextGoalQueue(page, {
      objective: 'Wait for the Claude goal route unlock.',
      clearCommand: '/goal clear',
    })
  })
})
