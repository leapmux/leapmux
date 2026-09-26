import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

const GOOSE = AgentProvider.GOOSE

gooseTest.describe('tracks the Goose to-do list', () => {
  // Goose's todo extension writes a markdown checklist. The sidebar draws the
  // items the extractor read off that checklist, not the markdown itself.
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
    await modelScript.waitForSteps()
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
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  })
})
