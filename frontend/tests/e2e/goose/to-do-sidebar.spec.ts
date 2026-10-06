import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

const GOOSE = AgentProvider.GOOSE

async function allowTodoWrite(page: Page, modelScript: ModelScript, step: number, item: string): Promise<void> {
  await modelScript.waitForSteps(step)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('todo: todo write')
  await expect(banner).toContainText(item)
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
}

gooseTest('the sidebar follows each checklist the agent writes, and keeps it after a reload', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
  void authenticatedGooseWorkspace
  await modelScript.queue(
    {
      toolCalls: [updateTodosToolCall(GOOSE, 'todos-first', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'Report their purpose', status: 'pending' },
      ])],
    },
    { text: 'The checklist is written.' },
  )
  await sendMessage(page, modelScript.prompt('Write a two-step to-do list.'))
  await allowTodoWrite(page, modelScript, 1, 'Inspect the repository')
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)

  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  const list = page.locator('[data-testid="goals-and-todos"]:visible')
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Report their purpose')
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
  await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)

  await modelScript.queue(
    {
      toolCalls: [updateTodosToolCall(GOOSE, 'todos-second', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'Report their purpose', status: 'completed' },
      ])],
    },
    { text: 'Every step is done.' },
  )
  await sendMessage(page, modelScript.prompt('Mark every step done.'))
  await allowTodoWrite(page, modelScript, 3, 'Report their purpose')
  await modelScript.waitForSteps(4)
  await waitForAgentIdle(page)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
})
