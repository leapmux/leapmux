import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

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
    const todos = goalsAndTodosList(page)
    await expandGoalsAndTodosSection(page)
    for (const step of ['Inspect the repository', 'List three checks', 'Report their purpose'])
      await expect(todos).toContainText(step)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    for (const step of ['Inspect the repository', 'List three checks', 'Report their purpose'])
      await expect(todos).toContainText(step)
  })
})
