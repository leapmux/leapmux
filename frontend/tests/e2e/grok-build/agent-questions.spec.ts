import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { chooseQuestionOptionWithNote, exerciseQuestionAnswer, exerciseQuestionPages, exerciseQuestionPreviews, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { messageBubbles } from '../helpers/ui'

grokTest.describe('control requests for Grok Build', () => {
  // Grok uses each question's text as its answer key.
  // The reply carries the reader's words beside the selected option. A note does not replace that option.
  grokTest('answers a question with a choice and a note', async ({ native }) => {
    const { page } = native
    const { result } = await exerciseQuestionAnswer(native, {
      questions: [
        {
          question: 'Which database?',
          header: 'Database',
          options: [
            { label: 'Postgres', description: 'Relational' },
            { label: 'Redis', description: 'In-memory' },
          ],
        },
      ],
      callId: 'grok-question',
      prompt: 'Ask me for a database.',
      answer: 'Postgres it is.',
      reply: chooseQuestionOptionWithNote(native, 'Postgres', 'Use version 16'),
    })

    expect(result).toContain('"Which database?"="Postgres"')
    expect(result).not.toContain('Redis')
    expect(result).toContain('user notes: Use version 16')
    await expect(messageBubbles(page).filter({ hasText: 'Which database?: Postgres, Use version 16' }).first()).toBeVisible()
  })
})

grokTest('preserves answers across native question pages and reloads', async ({ native }) => {
  await exerciseQuestionPages(native)
})

grokTest('preserves native option previews in a narrow layout after reload', async ({ native }) => {
  await exerciseQuestionPreviews(native)
})

grokTest('returns a custom native answer after its draft survives reload', async ({ native }) => {
  await exerciseTypedQuestionAnswer(native)
})

grokTest('keeps a native question on its owner tab and workspace after reload', async ({ native }) => {
  await exerciseQuestionIsolation(native, { customAnswerDraft: true })
})
