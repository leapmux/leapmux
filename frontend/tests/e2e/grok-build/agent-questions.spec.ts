import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { composerEditor, controlButton, messageBubbles, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'

/**
 * The encoded body of the request after the question.
 * Grok's reply holds quotes, and the encoded body escapes each of them whatever form the tool result content takes, so
 * the check reads the escaped pair. The question call that the body repeats lists every option, but never the pair of
 * the question and its answer.
 */
function encodedBody(request: MockModelRequestRecord): string {
  return JSON.stringify(request.body)
}

grokTest.describe('Grok Build control requests', () => {
  // Grok uses each question's text as its answer key.
  // The reply carries the reader's words beside the selected option. A note does not replace that option.
  grokTest('answers a question with a choice and a note', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{
        question: 'Which database?',
        header: 'Database',
        options: [{ label: 'Postgres', description: 'Relational' }, { label: 'Redis', description: 'In-memory' }],
      }],
      callId: 'grok-question',
      prompt: 'Ask me for a database.',
      answer: 'Postgres it is.',
      reply: async (banner) => {
        await banner.getByTestId('question-option-Postgres').click()
        await composerEditor(page).fill('Use version 16')
        await controlButton(page, 'submit').click()
      },
      readResult: encodedBody,
    })

    expect(result).toContain('\\"Which database?\\"=\\"Postgres\\"')
    expect(result).toContain('user notes: Use version 16')
    await expect(messageBubbles(page).filter({ hasText: 'Which database?: Postgres, Use version 16' }).first()).toBeVisible()
  })
})
