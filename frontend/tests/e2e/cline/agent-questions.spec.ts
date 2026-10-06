import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { nativeContext } from './scenarios'

/**
 * A real native question tool opens the shared question controls. The selected answer must reach the native model.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * The Worker answers Cline's native question executor. Its reply must reach the same native call.
 */
clineTest.describe('Cline control requests', () => {
  clineTest('answers a question with the option the reader picks', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    // The SECOND option: a result that states the first option, or no option at
    // all, fails the check below.
    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{
        question: 'Which database?',
        header: 'Database',
        options: [{ label: 'Postgres', description: 'Relational' }, { label: 'Redis', description: 'In-memory' }],
      }],
      callId: 'cline-question',
      prompt: 'Ask me for a database.',
      answer: 'Redis it is.',
      reply: chooseQuestionOption('Redis'),
    })
    // The worker answered Cline's question executor, and Cline gave the answer to the
    // model as the question's result. The whole request also holds the earlier call
    // arguments, which list every option, so the check reads the result alone.
    expect(result).toContain('Redis')
    expect(result).not.toContain('Postgres')
  })
})
