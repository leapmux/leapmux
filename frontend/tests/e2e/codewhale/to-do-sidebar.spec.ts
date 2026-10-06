import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { initialTodoList, TODO_LIST_STEPS } from '../helpers/todoSidebar'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codewhaleTest.describe('Codewhale to-do list', () => {
  codewhaleTest('shows the list that todo_write keeps, and keeps it after a reload', async ({ native }) => {
    const { page, modelScript } = native
    await modelScript.queue(
      { toolCalls: [updateTodosToolCall(AgentProvider.CODEWHALE, 'todo-call', initialTodoList(TODO_LIST_STEPS))] },
      { text: 'I wrote the plan.' },
    )
    await sendMessage(page, modelScript.prompt('Plan three steps to review this repository.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The worker reads the list that the runtime RETURNS, not the call's input,
    // so each row here is the runtime's own.
    const todos = goalsAndTodosList(page)
    await expandGoalsAndTodosSection(page)
    for (const step of TODO_LIST_STEPS)
      await expect(todos).toContainText(step)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    for (const step of TODO_LIST_STEPS)
      await expect(todos).toContainText(step)
  })
})
