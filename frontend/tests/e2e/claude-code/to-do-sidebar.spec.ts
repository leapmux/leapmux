import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

const CLAUDE = AgentProvider.CLAUDE_CODE

claudeTest.describe('Claude Code to-do sidebar', () => {
  // TodoWrite re-sends the whole list, so the second call replaces the first.
  // Claude suppresses the todo row in the transcript; the sidebar is the surface
  // the matrix documents.
  claudeTest('the sidebar follows each list the agent writes, and keeps it after a reload', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    await modelScript.queue(
      {
        toolCalls: [updateTodosToolCall(CLAUDE, 'todos-first', [
          { step: 'Inspect the repository', status: 'completed' },
          { step: 'List three checks', status: 'in_progress' },
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
    await expect(list).toContainText('List three checks')
    await expect(list).toContainText('Report their purpose')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)

    await modelScript.queue(
      {
        toolCalls: [updateTodosToolCall(CLAUDE, 'todos-second', [
          { step: 'Inspect the repository', status: 'completed' },
          { step: 'List three checks', status: 'completed' },
          { step: 'Report their purpose', status: 'completed' },
        ])],
      },
      { text: 'Every step is done.' },
    )
    await sendMessage(page, modelScript.prompt('Mark every step done.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(3)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(3)
  })
})
