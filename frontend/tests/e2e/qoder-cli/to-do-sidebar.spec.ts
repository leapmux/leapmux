import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'

qoderTest.describe('qoder CLI to-do sidebar', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('shows native WriteTodos state and restores it after reload', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await modelScript.queue(
      { toolCalls: [updateTodosToolCall(AgentProvider.QODER, 'qoder-todos-first', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'List three checks', status: 'in_progress' },
        { step: 'Report their purpose', status: 'pending' },
      ])] },
      { text: 'The first list is written.' },
    )
    await sendMessage(page, modelScript.prompt('Write a three-step to-do list.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the repository')
    await expect(list).toContainText('List three checks')
    await expect(list).toContainText('Report their purpose')
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
    await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)

    await modelScript.queue(
      { toolCalls: [updateTodosToolCall(AgentProvider.QODER, 'qoder-todos-second', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'List three checks', status: 'completed' },
        { step: 'Report their purpose', status: 'completed' },
      ])] },
      { text: 'Every step is done.' },
    )
    await sendMessage(page, modelScript.prompt('Mark every step done.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(3)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(3)
  })
})
