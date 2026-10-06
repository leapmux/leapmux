import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { createTestDirectory } from '../helpers/runDirectory'
import { assistantBubbles, chatScrollContainer, controlButton, focusComposer, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { PI_AGENT, piTest } from '../pi-fixtures'
import { nativeContext } from './scenarios'

for (const answerKind of ['custom', 'selected']) {
  piTest(`delivers a ${answerKind} answer to the real Pi question extension`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, PI_AGENT, { workingDir: createTestDirectory('renderer-pi-custom-') })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    // The EXTENSION is real -- it is what turns the tool call into a control
    // request and carries the answer back. Only the decision to ask is scripted.
    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{
        question: 'Choose a style',
        header: 'Style',
        options: [
          { label: 'Alpha', description: 'Use the first style.' },
          { label: 'Beta', description: 'Use the second style.' },
        ],
      }],
      callId: 'style-question',
      prompt: 'Ask me which style to use.',
      answer: 'Recorded the style.',
      reply: answerKind === 'custom'
        ? async () => {
          await focusComposer(page)
          await page.keyboard.insertText('A custom style')
          await controlButton(page, 'submit').click()
        }
        : chooseQuestionOption('Beta'),
    })
    expect(result).toContain(answerKind === 'custom' ? 'A custom style' : 'Beta')
    expect(result).not.toContain('Alpha')
    await expect(assistantBubbles(page).filter({ hasText: 'User has answered your questions:' })).toContainText(answerKind === 'custom' ? 'A custom style' : 'Beta')
    await expect(chatScrollContainer(page).getByText('User declined to answer questions', { exact: true })).toHaveCount(0)
  })
}
