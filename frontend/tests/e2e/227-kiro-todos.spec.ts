import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroCompleteTodosToolCall, updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

/**
 * 227 -- Kiro to-do list.
 *
 * Kiro keeps its list in its `todo_list` tool, and every call answers with the
 * whole list, so each finished call is a snapshot of it.
 */
kiroTest.describe('tracks the Kiro to-do list', () => {
  kiroTest('the sidebar follows the list the agent creates and completes, and keeps it after a reload', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue(
      {
        toolCalls: [updateTodosToolCall(KIRO, 'todos-create', [
          { step: 'Inspect the repository', status: 'pending' },
          { step: 'List three checks', status: 'pending' },
          { step: 'Report their purpose', status: 'pending' },
        ])],
      },
      { text: 'The plan is written.' },
    )
    await sendMessage(page, modelScript.prompt('Write a three-step to-do list.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the repository')
    await expect(list).toContainText('Report their purpose')
    await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(3)
    // The chat draws the call as the list it holds.
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('3 tasks', { exact: true }).first()).toBeVisible()

    await modelScript.queue(
      { toolCalls: [kiroCompleteTodosToolCall('todos-complete', ['1', '2'])] },
      { text: 'Two steps are done.' },
    )
    await sendMessage(page, modelScript.prompt('Mark the first two steps done.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
    await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  })
})
