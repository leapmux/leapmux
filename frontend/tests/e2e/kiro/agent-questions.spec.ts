import { expect } from '@playwright/test'
import { kiroUserText } from '../helpers/kiroSurface'
import { chooseQuestionOption, exerciseQuestionAnswer, exerciseTypedQuestionAnswer } from '../helpers/nativeQuestion'
import { exerciseQuestionIsolation } from '../helpers/nativeQuestionIsolation'
import { expectSettingsChip } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'
import { nativeContext } from './scenarios'

kiroTest.describe('control requests for Kiro', () => {
  // Kiro offers its question tool in a spec mode alone, and a spec mode first
  // classifies the prompt, which the housekeeping rules answer. The test chooses
  // the SECOND option, so an answer that Kiro never read cannot pass as the first.
  kiroTest('answers a question with a chosen option', async ({ page, askingKiroWorkspace, leapmuxServer, modelScript }) => {
    await expectSettingsChip(page, 'Spec')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingKiroWorkspace.workspaceId })

    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{
        question: 'Which database?',
        header: 'Database',
        options: [{ label: 'Postgres', description: 'Relational' }, { label: 'SQLite', description: 'Embedded' }],
      }],
      callId: 'kiro-question',
      prompt: 'Ask me for a database.',
      answer: 'SQLite it is.',
      reply: chooseQuestionOption('SQLite', context),
      // The call after the question carries the answer as the result of the question
      // tool. The history of the call also lists both options, so the test reads the
      // current message alone.
      readResult: request => kiroUserText(request.body),
    })
    expect(result).toContain('SQLite')
    expect(result).not.toContain('Postgres')
  })
})

kiroTest('returns a custom native answer after its draft survives reload', async ({ page, modelScript, leapmuxServer, askingKiroWorkspace }) => {
  await expectSettingsChip(page, 'Spec')
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingKiroWorkspace.workspaceId })
  await exerciseTypedQuestionAnswer(context, { readResult: request => kiroUserText(request.body) })
})

kiroTest('keeps a native question on its owner tab and workspace after reload', async ({ page, modelScript, leapmuxServer, askingKiroWorkspace }) => {
  await expectSettingsChip(page, 'Spec')
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingKiroWorkspace.workspaceId })
  await exerciseQuestionIsolation(context, { customAnswerDraft: true })
})
