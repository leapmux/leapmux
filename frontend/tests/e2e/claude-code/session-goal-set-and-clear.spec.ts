import { claudeTest } from '../claude-fixtures'
import { exerciseTextGoalQueue } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'

claudeTest.describe('claude session goal input queue', () => {
  claudeTest('routes session-goal commands through the input queue', async ({ native }) => {
    // The goal commands drive turns that this test does not count.
    await native.modelScript.fallback({ text: 'Understood.' })
    // Claude starts lazily. Its startup frame advertises /goal after this turn.
    await sendNativeAnswer(native, 'Reply with the single word: ready', 'ready')
    await exerciseTextGoalQueue(native, {
      objective: 'Wait for the Claude goal route unlock.',
      clearCommand: '/goal clear',
    })
  })
})
