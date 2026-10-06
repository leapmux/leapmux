import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

const COPILOT = AgentProvider.GITHUB_COPILOT

copilotTest('the sidebar follows each checklist the agent writes, and keeps it after a reload', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  void authenticatedCopilotWorkspace
  await modelScript.queue(
    {
      toolCalls: [updateTodosToolCall(COPILOT, 'todos-first', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'Report their purpose', status: 'pending' },
      ])],
    },
    { text: 'The checklist is written.' },
  )
  await sendMessage(page, modelScript.prompt('Write a two-step to-do list.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  const list = goalsAndTodosList(page)
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Report their purpose')
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
  await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)

  await modelScript.queue(
    {
      toolCalls: [updateTodosToolCall(COPILOT, 'todos-second', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'Report their purpose', status: 'completed' },
      ])],
    },
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
