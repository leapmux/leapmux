import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('Codex to-do sidebar', () => {
  codexTest('keeps the native plan update after a reload', async ({ native }) => {
    const { page, modelScript } = native
    await modelScript.queue(
      {
        toolCalls: [updateTodosToolCall(AgentProvider.CODEX, 'codex-todos', [
          { step: 'Inspect the input', status: 'completed' },
          { step: 'Check the output', status: 'in_progress' },
        ])],
      },
      { text: 'The checklist is ready.' },
    )
    await sendMessage(page, modelScript.prompt('Write a two-step checklist.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = goalsAndTodosList(page)
    await expect(list).toContainText('Inspect the input')
    await expect(list).toContainText('Check the output')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list).toContainText('Check the output')
  })
})
