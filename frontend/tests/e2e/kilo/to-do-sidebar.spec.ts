import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'

kiloTest('keeps the to-do list after a reload', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  await modelScript.queue(
    { toolCalls: [updateTodosToolCall(AgentProvider.KILO, 'kilo-todos', [
      { step: 'Inspect the repository', status: 'completed' },
      { step: 'Report the finding', status: 'in_progress' },
    ])] },
    { text: 'The list is ready.' },
  )
  await sendMessage(page, modelScript.prompt('Write a two-step to-do list.'))
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  const list = page.locator('[data-testid="goals-and-todos"]:visible')
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Report the finding')
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
  await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list).toContainText('Report the finding')
})
