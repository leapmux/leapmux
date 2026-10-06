import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { kiroCompleteTodosToolCall, updateTodosToolCall } from '../helpers/providerToolCalls'
import { chatScrollContainer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

const KIRO = AgentProvider.KIRO

kiroTest.describe('tracks the Kiro to-do list', () => {
  kiroTest('the sidebar follows the list the agent creates and completes, and keeps it after a reload', async ({ native }) => {
    const { page, modelScript } = native
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
    const list = goalsAndTodosList(page)
    await expect(list).toContainText('Inspect the repository')
    await expect(list).toContainText('Report their purpose')
    await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(3)
    // The chat draws the call as the list it holds.
    await expect(chatScrollContainer(page).getByText('3 tasks', { exact: true }).first()).toBeVisible()

    // Kiro completes tasks by their IDs and sends no whole list, and it has no in-progress state.
    // So this spec cannot use exerciseTodoListReplacement, which writes the whole list twice.
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
