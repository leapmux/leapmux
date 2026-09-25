import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

/**
 * 137 — Oh My Pi to-do list.
 *
 * omp's `todo` tool keeps a list of tasks in phases, and every call returns the
 * whole list in its result. The worker reads each result as a snapshot, and the
 * Goals & To-dos section draws it.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

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
    await waitForAgentIdle(page, 180_000)

    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible').first()
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
