import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution } from '../helpers/nativeCodeExecution'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { nativeContext } from './scenarios'

codexTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, { directoryPrefix: 'native-code-execution-' })
  await exerciseNativeCodeExecution(context, { scripts: marker => [
    { label: 'output', source: `text(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42`, failed: false },
    { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
  ], browserProof: async (rowId) => {
    const agent = await currentNativeAgent(context)
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    const pair = snapshot.messages.filter(message => message.spanId === rowId)
    expect(pair).toHaveLength(2)
    expect(pair[0]!.spanColor).toBeGreaterThan(0)
    expect(pair[1]!.spanColor).toBe(pair[0]!.spanColor)
    const lines: ({ span_id: string, type: string } | null)[] = JSON.parse(pair[1]!.spanLines)
    expect(lines).toContainEqual(expect.objectContaining({ span_id: rowId, type: 'connector_end' }))
    const column = lines.findIndex(line => line?.span_id === rowId)
    const row = page.locator('[data-span-columns]:visible').filter({ has: toolCallRow(page, rowId) })
    await expect(row.locator(`[data-span-line-column="${column}"]`)).toBeVisible()
  } })
})

codexTest('retains a completed native script with empty output after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const { workspaceId } = context
  await openNativeAgent(context, { directoryPrefix: 'native-code-empty-' })
  const callId = 'native-code-empty'
  const start = await modelScript.queue({ toolCalls: [codexExecToolCall(callId, 'text("");')] }, { text: 'The empty native script completed.' })
  await sendMessage(page, modelScript.prompt('Run the empty native script.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const result = nativeToolResult(await modelScript.requestAt(start + 1), callId)
  expect(result).toContain('Script completed')
  expect(result).not.toContain('Script failed')
  const bubble = toolCallRow(page, callId)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, workspaceId)
    }
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
    await expect(assistantBubbles(page).filter({ hasText: 'The empty native script completed.' }).first()).toBeVisible()
  }
})

codexTest('retains native large-output truncation and its computed head and tail after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const { workspaceId } = context
  await openNativeAgent(context, { directoryPrefix: 'native-code-large-' })
  const callId = 'native-code-large'
  const source = '// @exec: {"max_output_tokens": 300}\ntext(Array.from({ length: 3000 }, (_, i) => "NATIVELARGE" + i + ":" + "x".repeat(40)).join("\\n"));'
  const start = await modelScript.queue({ toolCalls: [codexExecToolCall(callId, source)] }, { text: 'The large native script completed.' })
  await sendMessage(page, modelScript.prompt('Run the large native script.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const result = nativeToolResult(await modelScript.requestAt(start + 1), callId)
  expect(result).toContain('Warning: truncated output')
  expect(result).toContain('NATIVELARGE0:')
  expect(result).toContain('NATIVELARGE2999:')
  expect(result).not.toContain('NATIVELARGE1500:')
  const bubble = toolCallRow(page, callId)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, workspaceId)
    }
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
    await expect(bubble).toContainText('Warning: truncated output')
    await expandNativeResultView(bubble)
    await expect(bubble).toContainText('NATIVELARGE0:')
    await expect(bubble).toContainText('NATIVELARGE2999:')
    await expect(bubble).not.toContainText('NATIVELARGE1500:')
  }
})

codexTest('omits outer execution rows when a nested shell command supplies the output', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, { directoryPrefix: 'native-code-nested-' })
  const callId = 'native-code-nested'
  const source = 'text(await tools.exec_command({cmd:"printf NESTED_RESULT",max_output_tokens:500}));'
  const start = await modelScript.queue({ toolCalls: [codexExecToolCall(callId, source)] }, { text: 'The nested shell command completed.' })
  await sendMessage(page, modelScript.prompt('Run the nested shell command.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain('NESTED_RESULT')
  const agent = await currentNativeAgent(context)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, context.workspaceId)
    }
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    expect(snapshot.messages.filter(message => message.spanId === callId)).toHaveLength(0)
    const command = snapshot.messages.find(message => message.spanType === 'commandExecution')
    expect(command).toBeDefined()
    await expect(toolCallRow(page, command!.spanId)).toContainText('NESTED_RESULT')
    await expect(toolCallRow(page, callId)).toHaveCount(0)
    for (const message of snapshot.messages)
      expect(message.spanLines).not.toContain(callId)
  }
})

codexTest('omits an empty shell poll without nested rows', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, { directoryPrefix: 'native-code-empty-poll-' })
  const callId = 'native-empty-poll'
  const start = await modelScript.queue(
    { toolCalls: [codexExecToolCall('native-poll-start', 'const r = await tools.exec_command({cmd:"read -r line",tty:true,yield_time_ms:1,max_output_tokens:500}); store("pollSession",r.session_id); text(r);')] },
    { toolCalls: [codexExecToolCall(callId, 'text(await tools.write_stdin({session_id:load("pollSession"),chars:"",yield_time_ms:1000,max_output_tokens:500}));')] },
    { toolCalls: [codexExecToolCall('native-poll-finish', 'text(await tools.write_stdin({session_id:load("pollSession"),chars:"\\n",yield_time_ms:1000,max_output_tokens:500}));')] },
    { text: 'The shell poll completed.' },
  )
  await sendMessage(page, modelScript.prompt('Start a shell reader, poll once, then finish it.'))
  await modelScript.waitForSteps(start + 4)
  await waitForAgentIdle(page)
  const result = nativeToolResult(await modelScript.requestAt(start + 2), callId)
  expect(result).toContain('Script completed')
  const blocks: { text: string }[] = JSON.parse(result)
  expect(JSON.parse(blocks[1]!.text)).toMatchObject({ output: '', original_token_count: 0 })
  const agent = await currentNativeAgent(context)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, context.workspaceId)
    }
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    expect(snapshot.messages.filter(message => message.spanId === callId)).toHaveLength(0)
    await expect(toolCallRow(page, callId)).toHaveCount(0)
  }
})

codexTest('omits a yielded execution with no output or nested rows', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, { directoryPrefix: 'native-code-empty-yield-' })
  const agent = await currentNativeAgent(context)
  try {
    const callId = 'native-empty-yield'
    const start = await modelScript.queue(
      { toolCalls: [codexExecToolCall(callId, 'yield_control(); await new Promise(() => {});')] },
      { text: 'The native script yielded without output.' },
    )
    await sendMessage(page, modelScript.prompt('Yield the native script without output.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain('Script running with cell ID')
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await openWorkspace(page, context.workspaceId)
      }
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      expect(snapshot.messages.filter(message => message.spanId === callId)).toHaveLength(0)
      await expect(toolCallRow(page, callId)).toHaveCount(0)
      await expect(assistantBubbles(page).filter({ hasText: 'The native script yielded without output.' }).first()).toBeVisible()
    }
  }
  finally {
    await closeNativeAgentAndWait(context, agent.id)
  }
})
