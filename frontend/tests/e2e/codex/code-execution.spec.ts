import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CODEX_ITEM } from '../../../src/generated/contracts/codex-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { codexTest } from '../codex-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution } from '../helpers/nativeCodeExecution'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { assistantBubbles, openWorkspace, sendMessage, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { codexAsyncExecRules } from './asyncExec'
import { nativeContext } from './scenarios'

codexTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, { directoryPrefix: 'native-code-execution-' })
  await exerciseNativeCodeExecution(context, {
    scripts: marker => [
      {
        label: 'output',
        source: `text(${JSON.stringify(marker)} + (40 + 2));`,
        expected: `${marker}42`,
        failed: false,
      },
      {
        label: 'failure',
        source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`,
        expected: `${marker}77`,
        failed: true,
      },
    ],
    browserProof: async (rowId) => {
      const agent = await currentNativeAgent(context)
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      const pair = snapshot.messages.filter(message => message.spanId === rowId)
      expect(pair).toHaveLength(2)
      expect(pair[0]!.spanColor).toBeGreaterThan(0)
      expect(pair[1]!.spanColor).toBe(pair[0]!.spanColor)
      const lines: ({ span_id: string, type: string } | null)[] = JSON.parse(pair[1]!.spanLines)
      expect(lines).toContainEqual(expect.objectContaining({ span_id: rowId, type: 'connector_end' }))
      const column = lines.findIndex(line => line?.span_id === rowId)
      const row = page.locator('[data-span-columns]:visible').filter({
        has: toolCallRow(page, rowId),
      })
      await expect(row.locator(`[data-span-line-column="${column}"]`)).toBeVisible()
    },
  })
})

codexTest('reports a native model without code-mode execution as failed after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, {
    directoryPrefix: 'native-code-unavailable-',
    overrides: { model: 'gpt-5.5' },
  })
  const callId = 'native-unsupported-exec'
  const start = await modelScript.queue({
    toolCalls: [
      codexExecToolCall(callId, 'text(40 + 2);'),
    ],
  }, { text: 'The native executor is unavailable for this model.' })
  await sendMessage(page, modelScript.prompt('Attempt the unsupported native execution call.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  expect((await modelScript.requestAt(start)).body).toMatchObject({ model: 'gpt-5.5' })
  const failure = 'unsupported custom tool call: exec'
  expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toBe(failure)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, context.workspaceId)
    }
    const row = toolCallRow(page, callId)
    await expect(row).toHaveCount(1)
    await expect(row).toHaveAttribute('data-tool-status', 'failed')
    await expect(row).toContainText(failure)
  }
})

codexTest('retains a completed native script with empty output after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  const { workspaceId } = context
  await openNativeAgent(context, { directoryPrefix: 'native-code-empty-' })
  const callId = 'native-code-empty'
  const start = await modelScript.queue({
    toolCalls: [
      codexExecToolCall(callId, 'text("");'),
    ],
  }, { text: 'The empty native script completed.' })
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
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  const { workspaceId } = context
  await openNativeAgent(context, { directoryPrefix: 'native-code-large-' })
  const callId = 'native-code-large'
  const source = '// @exec: {"max_output_tokens": 300}\ntext(Array.from({ length: 3000 }, (_, i) => "NATIVELARGE" + i + ":" + "x".repeat(40)).join("\\n"));'
  const start = await modelScript.queue({
    toolCalls: [
      codexExecToolCall(callId, source),
    ],
  }, { text: 'The large native script completed.' })
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
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, { directoryPrefix: 'native-code-nested-' })
  const callId = 'native-code-nested'
  const source = 'text(await tools.exec_command({cmd:"printf NESTED_RESULT",max_output_tokens:500}));'
  const start = await modelScript.queue({
    toolCalls: [
      codexExecToolCall(callId, source),
    ],
  }, { text: 'The nested shell command completed.' })
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

codexTest('retains independent script output beside a nested shell receipt after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, { directoryPrefix: 'native-code-mixed-' })
  const callId = 'native-code-mixed'
  const source = 'text(await tools.exec_command({cmd:"printf NESTED_MIXED",max_output_tokens:500})); text("INDEPENDENT_" + (40 + 2));'
  const start = await modelScript.queue({
    toolCalls: [
      codexExecToolCall(callId, source),
    ],
  }, { text: 'The mixed script completed.' })
  await sendMessage(page, modelScript.prompt('Run the script with independent output.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain('INDEPENDENT_42')
  const agent = await currentNativeAgent(context)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, context.workspaceId)
    }
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    expect(snapshot.messages.filter(message => message.spanId === callId)).toHaveLength(2)
    await expect(toolCallRow(page, callId)).toHaveCount(1)
    await expect(toolCallRow(page, callId)).toContainText('INDEPENDENT_42')
    await expect(toolCallRow(page, callId)).toContainText('NESTED_MIXED')
  }
})

codexTest('omits an empty shell poll without nested rows', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, { directoryPrefix: 'native-code-empty-poll-' })
  const callId = 'native-empty-poll'
  const start = await modelScript.queue(
    {
      toolCalls: [
        codexExecToolCall('native-poll-start', 'const r = await tools.exec_command({cmd:"read -r line",tty:true,yield_time_ms:1,max_output_tokens:500}); store("pollSession",r.session_id); text(r);'),
      ],
    },
    {
      toolCalls: [
        codexExecToolCall(callId, 'text(await tools.write_stdin({session_id:load("pollSession"),chars:"",yield_time_ms:1000,max_output_tokens:500}));'),
      ],
    },
    {
      toolCalls: [
        codexExecToolCall('native-poll-finish', 'text(await tools.write_stdin({session_id:load("pollSession"),chars:"\\n",yield_time_ms:1000,max_output_tokens:500}));'),
      ],
    },
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

/** Whether a stored message is the completion of the Codex command item whose command holds `marker`. */
function isCompletedCommand(message: AgentChatMessage, marker: string): boolean {
  const body = nativeMessageBody(message)
  return isObject(body) && isObject(body.item) && body.item.type === CODEX_ITEM.CommandExecution
    && body.item.status === 'completed' && body.item.exitCode === 0
    && typeof body.item.command === 'string' && body.item.command.includes(marker)
}

codexTest('omits a settled poll of an exited session and a running session', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  const { workingDir } = await openNativeAgent(context, { directoryPrefix: 'native-code-settled-poll-' })
  const agent = await currentNativeAgent(context)
  const callId = 'native-settled-poll'
  const gate = 'native-settled-poll'
  const flagName = 'native-settled-poll-exit.flag'
  const flag = join(workingDir, flagName)
  const exitCommand = `while [ ! -e ${quotePosixShellArgument(flag)} ]; do sleep 0.05; done`
  const start = await modelScript.queue(
    {
      toolCalls: [
        codexExecToolCall('native-settled-poll-start', `const exited = await tools.exec_command({cmd:${JSON.stringify(exitCommand)},yield_time_ms:250,max_output_tokens:500}); const running = await tools.exec_command({cmd:"read -r line",tty:true,yield_time_ms:250,max_output_tokens:500}); store("exitedSession",exited.session_id); store("runningSession",running.session_id); text({exited,running});`),
      ],
    },
    // The gate holds the poll until the Worker stores the completion of the exited session. No typed row then
    // arrives during the poll, so only its printed receipts can suppress its pair.
    {
      gate,
      toolCalls: [
        codexExecToolCall(callId, 'const r=await Promise.allSettled([tools.write_stdin({session_id:load("exitedSession"),chars:"",yield_time_ms:1000,max_output_tokens:1000}),tools.write_stdin({session_id:load("runningSession"),chars:"",yield_time_ms:1000,max_output_tokens:1000})]);for(let i=0;i<r.length;i++)text({i,...r[i]});'),
      ],
    },
    {
      toolCalls: [
        codexExecToolCall('native-settled-poll-finish', 'text(await tools.write_stdin({session_id:load("runningSession"),chars:"\\n",yield_time_ms:1000,max_output_tokens:500}));'),
      ],
    },
    { text: 'The settled shell poll completed.' },
  )
  try {
    await sendMessage(page, modelScript.prompt('Start two shell sessions, poll both, then finish the reader.'))
    await modelScript.waitForGate(gate)
    writeFileSync(flag, '')
    await retryUntilPass(async () => {
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      expect(snapshot.messages.some(message => isCompletedCommand(message, flagName)), 'the Worker holds the completion of the exited session').toBe(true)
    })
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)
  }
  finally {
    await modelScript.releaseGateIfHeld(gate)
  }
  const blocks: { text: string }[] = JSON.parse(nativeToolResult(await modelScript.requestAt(start + 2), callId))
  expect(blocks).toHaveLength(3)
  expect(blocks[0]!.text).toContain('Script completed')
  const exited = JSON.parse(blocks[1]!.text)
  expect(exited).toMatchObject({
    i: 0,
    status: 'fulfilled',
    value: { exit_code: 0, original_token_count: 0, output: '' },
  })
  expect(exited.value).not.toHaveProperty('session_id')
  expect(JSON.parse(blocks[2]!.text)).toMatchObject({
    i: 1,
    status: 'fulfilled',
    value: {
      session_id: expect.any(Number),
      original_token_count: 0,
      output: '',
    },
  })
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

codexTest('preserves numeric results and duplicate keys beside empty polls after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, { directoryPrefix: 'native-code-poll-content-' })
  const agent = await currentNativeAgent(context)
  const numericCall = 'native-poll-numeric-content'
  const duplicateCall = 'native-poll-duplicate-content'
  const containerCall = 'native-poll-output-container'
  const pollSource = 'const receipt = await tools.write_stdin({session_id:load("contentPollSession"),chars:"",yield_time_ms:1000,max_output_tokens:500});'
  const start = await modelScript.queue(
    {
      toolCalls: [
        codexExecToolCall('native-content-poll-start', 'const r = await tools.exec_command({cmd:"read -r line",tty:true,yield_time_ms:250,max_output_tokens:500}); store("contentPollSession",r.session_id); text(r);'),
      ],
    },
    {
      toolCalls: [
        codexExecToolCall(numericCall, `${pollSource} text({failureCount: 3, result: receipt});`),
      ],
    },
    {
      toolCalls: [
        codexExecToolCall(duplicateCall, `${pollSource} text('{"result":"POLL_FAILURE_DETAIL","result":' + JSON.stringify(receipt) + '}');`),
      ],
    },
    {
      toolCalls: [
        codexExecToolCall(containerCall, `${pollSource} text({output: receipt});`),
      ],
    },
    {
      toolCalls: [
        codexExecToolCall('native-content-poll-finish', 'text(await tools.write_stdin({session_id:load("contentPollSession"),chars:"\\n",yield_time_ms:1000,max_output_tokens:500}));'),
      ],
    },
    { text: 'The poll content checks completed.' },
  )
  try {
    await sendMessage(page, modelScript.prompt('Print numeric results and duplicate JSON keys beside empty shell polls.'))
    await modelScript.waitForSteps(start + 6)
    await waitForAgentIdle(page)
    for (const [offset, callId] of [
      [2, numericCall],
      [3, duplicateCall],
      [4, containerCall],
    ] as const) {
      const blocks: { text: string }[] = JSON.parse(nativeToolResult(await modelScript.requestAt(start + offset), callId))
      expect(blocks).toHaveLength(2)
      const printed = JSON.parse(blocks[1]!.text)
      expect(printed.result ?? printed.output).toMatchObject({
        session_id: expect.any(Number),
        original_token_count: 0,
        output: '',
      })
    }
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await openWorkspace(page, context.workspaceId)
      }
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      for (const [callId, content] of [
        [numericCall, '"failureCount":3'],
        [duplicateCall, '"result":"POLL_FAILURE_DETAIL","result":'],
      ] as const) {
        expect(snapshot.messages.filter(message => message.spanId === callId)).toHaveLength(2)
        await expect(toolCallRow(page, callId)).toHaveCount(1)
        await expect(toolCallRow(page, callId)).toContainText(content)
      }
      expect(snapshot.messages.filter(message => message.spanId === containerCall)).toHaveLength(0)
      await expect(toolCallRow(page, containerCall)).toHaveCount(0)
    }
  }
  finally {
    await closeNativeAgentAndWait(context, agent.id)
  }
})

codexTest('omits a yielded execution with no output or nested rows', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, { directoryPrefix: 'native-code-empty-yield-' })
  const agent = await currentNativeAgent(context)
  try {
    const callId = 'native-empty-yield'
    const start = await modelScript.queue(
      {
        toolCalls: [
          codexExecToolCall(callId, 'yield_control(); await new Promise(() => {});'),
        ],
      },
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

// A cell that yields keeps running, and the model reads the rest of its output through
// a wait call. Each yield returns only the output since the yield before it, so the
// card joins the output of the exec call and of the wait. The card states neither
// the yield header nor a second row for the wait.
codexTest('joins the output that a waited cell states after its yield into one card after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, { directoryPrefix: 'native-code-waited-cell-' })
  const agent = await currentNativeAgent(context)
  try {
    const callId = 'native-waited-cell'
    await modelScript.rule(...codexAsyncExecRules())
    const start = await modelScript.queue(
      {
        toolCalls: [
          codexExecToolCall(callId, 'text("CELLPARTONE"); yield_control(); await new Promise(resolve => setTimeout(resolve, 300)); text("CELLPARTTWO");'),
        ],
      },
      { text: 'The waited native cell completed.' },
    )
    await sendMessage(page, modelScript.prompt('Run the waited native cell.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    const finalRequest = await modelScript.requestAt(start + 1)
    expect(nativeToolResult(finalRequest, callId), 'the exec call yields').toContain('Script running with cell ID')
    expect(nativeToolResult(finalRequest, 'await-native-cell'), 'the wait reads the rest of the cell').toContain('CELLPARTTWO')
    const bubble = toolCallRow(page, callId)
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await openWorkspace(page, context.workspaceId)
      }
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      expect(snapshot.messages.filter(message => message.spanId === callId), 'the cell is one request row and one result row').toHaveLength(2)
      await expect(bubble).toHaveCount(1)
      await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
      await expect(bubble).toContainText('CELLPARTONE')
      await expect(bubble).toContainText('CELLPARTTWO')
      await expect(bubble).not.toContainText('Script running with cell ID')
      await expect(assistantBubbles(page).filter({ hasText: 'The waited native cell completed.' }).first()).toBeVisible()
    }
  }
  finally {
    await closeNativeAgentAndWait(context, agent.id)
  }
})
