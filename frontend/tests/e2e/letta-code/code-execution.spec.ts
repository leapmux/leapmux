import { expect } from '@playwright/test'
import { expectNativeCodeExecutionAbsent, nativeCodeExecutionSchema, openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { nativeModelConversationTurns, nativeToolOutcome } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeWorkflowScripts } from '../helpers/nativeWorkflowScripts'
import { nativeXmlField } from '../helpers/nativeXml'
import { waitForNewestModelRequest } from '../helpers/newestModelRequest'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { messageContents, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('distinguishes the native Workflow executor from absent standalone interpreters', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await openNativeCatalogTurn(context)
  nativeCodeExecutionSchema(request, 'Workflow', { script: 'string' })
  expectNativeCodeExecutionAbsent(request, ['codemode', 'exec', 'eval', 'REPL', 'js_execution', 'code_execution', 'execute_tools', 'mcp__node_repl__js'])
})

lettaTest('executes native Workflow scripts and retains computed results and errors after reload', async ({ native }) => {
  const marker = uniqueMarker('LETTACODE')
  await native.modelScript.fallback({ text: 'The native Workflow notification arrived.' })
  for (const { callId, source, expected, failed } of nativeWorkflowScripts(marker)) {
    expect(source).not.toContain(expected)
    const call = codeExecutionToolCall(native.provider, callId, source)
    const { toolRequest, resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [call],
      prompt: 'Execute the native Workflow script.',
      answer: 'The native Workflow launch ended.',
    })
    nativeCodeExecutionSchema(toolRequest, 'Workflow', { script: 'string' })
    const launch = (await nativeToolOutcome(native, resultRequest, call.id)).text
    const taskIds = [...launch.matchAll(/Workflow launched in background\. Task ID: (workflow_\d+)/g)]
    expect(taskIds).toHaveLength(1)
    const taskId = taskIds[0]![1]!
    const notification = await waitForNewestModelRequest(native.modelScript, (request) => {
      const last = nativeModelConversationTurns(request).filter(turn => turn.role === 'user').at(-1)?.text
      return last?.includes('<task-notification>') && nativeXmlField(last, 'task-id') === taskId && last.includes(expected) ? last : null
    })
    expect(notification).toContain(`<status>${failed ? 'failed' : 'completed'}</status>`)
    await waitForAgentIdle(native.page)
    await expect(messageContents(native.page).filter({ hasText: expected }).first()).toBeVisible()
    await native.page.reload()
    await expect(messageContents(native.page).filter({ hasText: expected }).first()).toBeVisible()
  }
})
