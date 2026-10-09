import { expect } from '@playwright/test'
import { museItem } from '../../../src/components/chat/providers/muse/protocol'
import { MUSE_ITEM_STATUS } from '../../../src/generated/contracts/muse-protocol'
import { AgentProvider, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { hasNativeToolResult, nativeToolResultContent } from '../helpers/nativeToolResult'
import { nativeToolRowId } from '../helpers/nativeToolRowId'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseTodoListReplacement, TODO_LIST_STEPS } from '../helpers/todoSidebar'
import { chatScrollContainer } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('shows native to-do changes and keeps their transcript after clearing and reload', async ({ native }) => {
  let firstItemId = ''
  await exerciseTodoListReplacement(native, {
    afterFirstList: async () => {
      firstItemId = await nativeToolRowId(native, 'todos-first')
    },
  })
  const secondItemId = await nativeToolRowId(native, 'todos-second')
  expect(firstItemId).not.toBe('')
  expect(secondItemId).not.toBe(firstItemId)
  await runNativeToolTurn(native, {
    toolCalls: [updateTodosToolCall(native.provider, 'todos-clear', [])],
    prompt: 'Clear the complete native to-do list.',
    answer: 'The native list is empty.',
    permissions: 'none',
  })
  await expect(goalsAndTodosList(native.page).locator('[data-task-checkbox]')).toHaveCount(0)
  const agent = await currentNativeAgent(native)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  const status = await native.modelScript.status()
  for (const callId of ['todos-first', 'todos-second', 'todos-clear']) {
    const itemId = await nativeToolRowId(native, callId)
    const items = snapshot.messages.filter(message => message.spanId === itemId && message.agentSessionId === snapshot.agentSessionId
      && message.agentProvider === AgentProvider.MUSE_CODE && message.source === MessageSource.AGENT)
      .map(message => museItem(nativeMessageBody(message)))
      .filter(item => item?.callId === callId && item.status === MUSE_ITEM_STATUS.Completed)
    expect(items).toHaveLength(1)
    const request = status.requests.find(record => hasNativeToolResult(record, callId))
    nativeToolResultContent(request, callId)
  }
  await native.page.reload()
  await expandGoalsAndTodosSection(native.page)
  await expect(goalsAndTodosList(native.page).locator('[data-task-checkbox]')).toHaveCount(0)
  for (const text of TODO_LIST_STEPS)
    await expect(chatScrollContainer(native.page)).toContainText(text)
})
