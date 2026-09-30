import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { codebuddyTaskCreateToolCall, codebuddyTaskUpdateToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code to-do sidebar', () => {
  codebuddyTest('keeps native task updates after reload and clears the completed list', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue(
      { toolCalls: [codebuddyTaskCreateToolCall('create-inspect', 'Inspect the repository', 'Inspect the files.')] },
      { toolCalls: [codebuddyTaskCreateToolCall('create-report', 'Report the result', 'Report the findings.')] },
      { toolCalls: [codebuddyTaskUpdateToolCall('complete-inspect', '1', 'completed')] },
      { text: 'The checklist is ready.' },
    )
    await sendMessage(page, modelScript.prompt('Write a two-step checklist.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)
    await expect(list).toContainText('Report the result')

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)

    await modelScript.queue(
      { toolCalls: [codebuddyTaskUpdateToolCall('complete-report', '2', 'completed')] },
      { text: 'The checklist is complete.' },
    )
    await sendMessage(page, modelScript.prompt('Mark the report step complete.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The checklist is complete.' })).toBeVisible()
    await expect(list.locator('[data-task-checkbox]')).toHaveCount(0)

    await page.reload()
    await expect(list.locator('[data-task-checkbox]')).toHaveCount(0)
  })
})
