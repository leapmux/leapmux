import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex to-do sidebar', () => {
  codexTest('keeps the native plan update after a reload', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await modelScript.queue(
      { toolCalls: [updateTodosToolCall(AgentProvider.CODEX, 'codex-todos', [
        { step: 'Inspect the input', status: 'completed' },
        { step: 'Check the output', status: 'in_progress' },
      ])] },
      { text: 'The checklist is ready.' },
    )
    await sendMessage(page, modelScript.prompt('Write a two-step checklist.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the input')
    await expect(list).toContainText('Check the output')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list).toContainText('Check the output')
  })
})
