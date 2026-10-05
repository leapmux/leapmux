import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall, junieAnswerToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from '../junie-fixtures'

junieTest.describe('Junie questions', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.JUNIE

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
    await waitForAgentIdle(page)

    // The native question creates a control request.
    // Open its choice menu to read the available answers.
    await expect(page.getByText('Which one?').first()).toBeVisible()
    await page.getByRole('button', { name: 'Pick' }).click()
    await expect(page.getByText('First').filter({ visible: true }).first()).toBeVisible()
    await expect(page.getByText('Second').filter({ visible: true }).first()).toBeVisible()

    // The turn waits for approval. The next native model request must carry
    // the selected choice, while the scripted answer stays neutral.
    await page.getByText('Second').filter({ visible: true }).first().click()
    await page.getByRole('button', { name: 'Approve' }).click()
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const answer = nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'junie-question')
    expect(answer).toContain('Second')
    expect(answer).not.toContain('First')
    await expect(assistantBubbles(page).filter({ hasText: 'You picked.' }).first()).toBeVisible()
  })
})
