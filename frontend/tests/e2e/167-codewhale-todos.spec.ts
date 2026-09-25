import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, expect } from './codewhale-fixtures'
import { updateTodosToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale to-do list', () => {
  codewhaleTest('shows the list that todo_write keeps, and keeps it after a reload', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue(
      {
        toolCalls: [updateTodosToolCall(AgentProvider.CODEWHALE, 'todo-call', [
          { step: 'Inspect the repository', status: 'completed' },
          { step: 'List three checks', status: 'in_progress' },
          { step: 'Report their purpose', status: 'pending' },
        ])],
      },
      { text: 'I wrote the plan.' },
    )
    await sendMessage(page, modelScript.prompt('Plan three steps to review this repository.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The worker reads the list that the runtime RETURNS, not the call's input,
    // so each row here is the runtime's own.
    const todos = page.locator('[data-testid="goals-and-todos"]:visible')
    await expandGoalsAndTodosSection(page)
    for (const step of ['Inspect the repository', 'List three checks', 'Report their purpose'])
      await expect(todos).toContainText(step)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    for (const step of ['Inspect the repository', 'List three checks', 'Report their purpose'])
      await expect(todos).toContainText(step)
  })
})
