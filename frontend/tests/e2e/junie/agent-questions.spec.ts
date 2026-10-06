import { exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, junieTest } from '../junie-fixtures'
import { JUNIE_AGENT, nativeContext } from './scenarios'

junieTest.describe('Junie questions', () => {
  junieTest('ask_user raises a permission-shaped choice', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    // The turn waits for approval. The next native model request must carry
    // the selected choice, while the scripted answer stays neutral.
    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{
        question: 'Which one?',
        header: 'Pick',
        options: [
          { label: 'First', description: 'The first choice.' },
          { label: 'Second', description: 'The second choice.' },
        ],
      }],
      callId: 'junie-question',
      prompt: 'Ask me which one.',
      answer: 'You picked.',
      // The native question creates a permission request. Its choice menu holds the available answers.
      reply: async () => {
        await page.getByRole('button', { name: 'Pick' }).click()
        await expect(page.getByText('First').filter({ visible: true }).first()).toBeVisible()
        await expect(page.getByText('Second').filter({ visible: true }).first()).toBeVisible()
        await page.getByText('Second').filter({ visible: true }).first().click()
        await page.getByRole('button', { name: 'Approve' }).click()
      },
    })
    expect(result).toContain('Second')
    expect(result).not.toContain('First')
  })
})
