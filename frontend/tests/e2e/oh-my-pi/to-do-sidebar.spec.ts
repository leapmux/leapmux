import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * A real native to-do tool updates the authoritative Worker snapshot. The sidebar must follow each update.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Oh My Pi's todo tool keeps tasks in phases. Each result contains the complete list. The Worker reads that result as a snapshot.
 */
const STEPS = ['Inspect the repository', 'List three checks', 'Report their purpose']

ohMyPiTest.describe('Oh My Pi to-do list', () => {
  ohMyPiTest('draws the list that a todo call opens', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // `init` states no status. omp marks the first task in progress itself.
    await modelScript.queue(
      { toolCalls: [updateTodosToolCall(AgentProvider.OH_MY_PI, 'plan-1', STEPS.map(step => ({ step, status: 'pending' as const })))] },
      { text: 'The plan is ready.' },
    )
    await sendMessage(page, modelScript.prompt('Plan the inspection of this repository.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = goalsAndTodosList(page)
    for (const step of STEPS)
      await expect(list).toContainText(step)
    // The list keeps omp's order, which is the order of the call.
    const text = await list.textContent() ?? ''
    expect(text.indexOf(STEPS[0]!)).toBeLessThan(text.indexOf(STEPS[1]!))
    expect(text.indexOf(STEPS[1]!)).toBeLessThan(text.indexOf(STEPS[2]!))
    // The statuses are omp's: the first task runs, and the rest wait.
    await expect(list.locator('[data-task-checkbox]')).toHaveCount(STEPS.length)
    await expect(list.locator('[data-task-checkbox]').nth(0)).toHaveAttribute('data-task-checkbox', 'in_progress')
    await expect(list.locator('[data-task-checkbox]').nth(1)).toHaveAttribute('data-task-checkbox', 'pending')
    await expect(list.locator('[data-task-checkbox]').nth(2)).toHaveAttribute('data-task-checkbox', 'pending')
  })
})
