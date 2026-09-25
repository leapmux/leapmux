import { mimoTaskToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection } from './helpers/subagentRegistry'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code to-do list', () => {
  // MiMo's to-do tool acts on one item for each call, and states the item's id
  // and its new status. The worker folds each call into the agent's list, so the
  // sidebar holds both items with the status the last call gave each one.
  mimoTest('folds each task call into the sidebar list', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [mimoTaskToolCall('create-inspect', { action: 'create', summary: 'Inspect the parser' })] },
      { toolCalls: [mimoTaskToolCall('create-report', { action: 'create', summary: 'Write the report' })] },
      { toolCalls: [mimoTaskToolCall('start-inspect', { action: 'start', id: 'T1' })] },
      { toolCalls: [mimoTaskToolCall('finish-inspect', { action: 'done', id: 'T1' })] },
      { text: 'The parser is inspected, and the report remains.' },
    )
    await sendMessage(page, modelScript.prompt('Track two tasks and finish the first one.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the parser')
    await expect(list).toContainText('Write the report')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)
    // The transcript draws each call as a to-do row with the item it acted on.
    await expect(messageContents(page).filter({ hasText: 'Write the report' }).first()).toBeVisible()
  })
})
