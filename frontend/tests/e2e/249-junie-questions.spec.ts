import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { askUserQuestionToolCall, junieAnswerToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.JUNIE

junieTest.describe('Junie questions', () => {
  // Junie's `ask_user` tool arrives over ACP as a PERMISSION-shaped request
  // with one option per choice: the question and the permissions share one
  // channel in this build.
  junieTest('ask_user raises a permission-shaped choice', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    await modelScript.rule(
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Choice task' } },
    )
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'junie-question', [{
          question: 'Which one?',
          header: 'Pick',
          options: [
            { label: 'First', description: 'The first choice.' },
            { label: 'Second', description: 'The second choice.' },
          ],
        }])],
      },
      { toolCalls: [junieAnswerToolCall('junie-answer', 'You picked.')] },
    )
    await sendMessage(page, modelScript.prompt('Ask me which one.'))
    await waitForAgentIdle(page, 120_000)

    // The question surfaces as a control request, not a tool row. Its choices
    // sit behind the request's select (the options of the permission-shaped
    // request), so the test opens that select to read them.
    await expect(page.getByText('Which one?').first()).toBeVisible()
    await page.getByRole('button', { name: 'Pick' }).click()
    await expect(page.getByText('First').filter({ visible: true }).first()).toBeVisible()
    await expect(page.getByText('Second').filter({ visible: true }).first()).toBeVisible()

    // The turn waits on the answer: pick a choice and approve the request, and
    // the scripted answer turn runs and states what the user picked.
    await page.getByText('First').filter({ visible: true }).first().click()
    await page.getByRole('button', { name: 'Approve' }).click()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'You picked.' }).first()).toBeVisible()
  })
})
