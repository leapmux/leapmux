import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { kiroUserText } from '../helpers/kiroSurface'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { expectSettingsChip, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { nativeContext } from './scenarios'

kiroTest.describe('Kiro control requests', () => {
  // Kiro offers its question tool in a spec mode alone, and a spec mode first
  // classifies the prompt, which the housekeeping rules answer. The test chooses
  // the SECOND option, so an answer that Kiro never read cannot pass as the first.
  kiroTest('answers a question with a chosen option', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'spec' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Spec')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{
        question: 'Which database?',
        header: 'Database',
        options: [{ label: 'Postgres', description: 'Relational' }, { label: 'SQLite', description: 'Embedded' }],
      }],
      callId: 'kiro-question',
      prompt: 'Ask me for a database.',
      answer: 'SQLite it is.',
      reply: chooseQuestionOption('SQLite'),
      // The call after the question carries the answer as the result of the question
      // tool. The history of the call also lists both options, so the test reads the
      // current message alone.
      readResult: request => kiroUserText(request.body),
    })
    expect(result).toContain('SQLite')
    expect(result).not.toContain('Postgres')
  })
})
