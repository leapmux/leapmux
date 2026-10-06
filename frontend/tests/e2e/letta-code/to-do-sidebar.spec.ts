import { lettaTaskCreateToolCall, lettaTaskListToolCall, lettaTaskUpdateToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'

lettaTest.describe('tracks the Letta Code to-do list', () => {
  lettaTest('the sidebar follows task creation and updates, and keeps them after a reload', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [lettaTaskCreateToolCall('create-first', 'Inspect the repository', 'Read the repository files.')] },
      { toolCalls: [lettaTaskCreateToolCall('create-second', 'List three checks', 'List three checks to run.')] },
      { toolCalls: [lettaTaskUpdateToolCall('complete-first', 'task_1', 'completed')] },
      { toolCalls: [lettaTaskUpdateToolCall('start-second', 'task_2', 'in_progress')] },
      { toolCalls: [lettaTaskListToolCall('list-tasks')] },
      { text: 'The list is written.' },
    )
    await sendMessage(page, modelScript.prompt('Write a three-step to-do list.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the repository')
    await expect(list).toContainText('List three checks')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)

    await modelScript.queue(
      { toolCalls: [lettaTaskUpdateToolCall('complete-second', 'task_2', 'completed')] },
      { text: 'Every step is done.' },
    )
    await sendMessage(page, modelScript.prompt('Mark every step done.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  })
})
