import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { cssAttributeValue } from '../helpers/cssAttribute'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, railedRows, toolCallRow } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('qoder CLI tool execution', () => {
  qoderTest('runs a Bash tool and draws its span', async ({ native }) => {
    const { page } = native
    const call = bashToolCall(native.provider, 'call-1', 'echo hi')
    await runNativeToolTurn(native, { toolCalls: [call], prompt: 'Run echo hi.', answer: 'The command ran.' })

    await expect(assistantBubbles(page).filter({ hasText: 'The command ran.' }).first()).toBeVisible()
    // A tool call opens a span, so a row of this call draws a rail.
    await expect(railedRows(page).filter({ has: page.locator(`[data-tool-call-id="${cssAttributeValue(call.id)}"]`) }).first()).toBeVisible()
  })
})

qoderTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})

qoderTest('keeps two native Bash calls and their different commands and outputs', async ({ native }) => {
  const { page } = native
  const proofs = [
    { call: bashToolCall(native.provider, 'native-qoder-first-command', 'node -e "process.stdout.write(\'QODERFIRST\' + (40 + 2))"'), output: 'QODERFIRST42' },
    { call: bashToolCall(native.provider, 'native-qoder-second-command', 'node -e "process.stdout.write(\'QODERSECOND\' + (70 + 7))"'), output: 'QODERSECOND77' },
  ]
  const calls = proofs.map(proof => proof.call)
  const initial = await currentNativeAgent(native)
  // The turn reads the request that follows the tool step, and that read fails
  // when the two calls produced no next model request.
  const { resultRequest: request } = await runNativeToolTurn(native, {
    toolCalls: calls,
    prompt: 'Run both native Bash calls in one model response.',
    answer: 'Both native commands finished.',
  })
  expect(request.mockCredential?.accepted).toBe(true)
  const current = await currentNativeAgent(native)
  expect(current.id).toBe(initial.id)
  expect(current.agentSessionId).toBe(initial.agentSessionId)
  const snapshot = await readNativeMessageSnapshot(native, current.id)
  for (const { call, output } of proofs) {
    if (!isObject(call.arguments) || typeof call.arguments.command !== 'string' || !call.arguments.command.trim())
      throw new Error('The scripted Qoder Bash call requires a nonempty command.')
    const command = call.arguments.command
    expect(nativeToolResult(request, call.id)).toBe(output)
    const rows = snapshot.messages.filter(message => message.agentSessionId === current.agentSessionId && message.spanId === call.id)
    expect(rows).toHaveLength(2)
    const originals = rows.map(nativeMessageBody)
    const blocks = originals.filter(isObject).flatMap(frame => isObject(frame.message) && Array.isArray(frame.message.content) ? frame.message.content.filter(isObject) : [])
    const opener = blocks.filter(block => block.type === 'tool_use' && block.id === call.id)
    const result = blocks.filter(block => block.type === 'tool_result' && block.tool_use_id === call.id)
    expect(opener).toHaveLength(1)
    expect(result).toHaveLength(1)
    const input = isObject(opener[0]?.input) ? opener[0].input : undefined
    expect(input?.command).toBe(command)
    expect(result[0]?.content).toBe(output)
    const requestRow = toolCallRow(page, call.id, 'request')
    const resultRow = toolCallRow(page, call.id)
    await expect(requestRow).toHaveCount(1)
    await expect(resultRow).toHaveCount(1)
    await expect(requestRow).toContainText(command)
    await expect(resultRow).toContainText(output)
    await expect(resultRow).toHaveAttribute('data-tool-status', 'completed')
  }
})
