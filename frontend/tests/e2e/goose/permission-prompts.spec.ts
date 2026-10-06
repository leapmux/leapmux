import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { fillMcpProbeForm, waitForMcpProbeFormDraft } from '../helpers/mcpProbeForm'
import { mcpToolCall, updateTodosToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { exerciseGoosePermissionRemoval } from './permissionScenario'

const GOOSE = AgentProvider.GOOSE

async function allowTodoWrite(page: Page, modelScript: ModelScript, step: number, item: string): Promise<void> {
  await modelScript.waitForSteps(step)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('todo: todo write')
  await expect(banner).toContainText(item)
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
}

gooseTest('permission-prompts: the sidebar follows each checklist the agent writes, and keeps it after a reload', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
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
  const list = goalsAndTodosList(page)
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

gooseTest('permission-prompts: roundtrips zero, false, and blue through native form elicitation', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  void authenticatedGooseWorkspace
  await modelScript.queue(
    { toolCalls: [mcpToolCall(AgentProvider.GOOSE, 'goose-form', { server: 'form_probe', tool: 'ask', input: {} })] },
    { text: 'The Goose form completed.' },
  )
  await sendMessage(page, modelScript.prompt('Call the form_probe ask tool exactly once.'))
  await modelScript.waitForSteps(1)
  const permission = page.getByTestId('control-banner').filter({ visible: true })
  await expect(permission).toContainText('form probe: ask')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()

  const form = await fillMcpProbeForm(page)
  // The page saves the last answer through a write queue. A reload that overtakes the write drops that answer.
  await waitForMcpProbeFormDraft(page, leapmuxServer.adminUserId)
  await page.reload()
  await expect(form.getByLabel('Count *')).toHaveValue('0')
  await expect(form.getByRole('button', { name: 'Enabled *', exact: true })).toHaveText('No')
  await expect(form.getByRole('button', { name: 'Color *', exact: true })).toHaveText('Blue')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_OK')
  await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
  await expect(form).toHaveCount(0)
})

gooseTest('permission-prompts: smart mode asks before a removal and auto mode runs it', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await exerciseGoosePermissionRemoval(context)
})
