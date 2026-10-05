import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { exerciseShellToolExecution, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest.describe('qoder CLI tool execution', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('runs a Bash tool and draws its span', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const call = bashToolCall(AgentProvider.QODER, 'call-1', 'echo hi')
    await modelScript.queue({
      toolCalls: [call],
    })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt('Run echo hi.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(assistantBubbles(page).filter({ hasText: 'The command ran.' }).first()).toBeVisible()
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
  })
})

qoderTest('runs successful and failed native commands with their actual output', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})

qoderTest('keeps two native Bash calls and their different commands and outputs', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  const proofs = [
    { call: bashToolCall(AgentProvider.QODER, 'native-qoder-first-command', 'node -e "process.stdout.write(\'QODERFIRST\' + (40 + 2))"'), output: 'QODERFIRST42' },
    { call: bashToolCall(AgentProvider.QODER, 'native-qoder-second-command', 'node -e "process.stdout.write(\'QODERSECOND\' + (70 + 7))"'), output: 'QODERSECOND77' },
  ]
  const calls = proofs.map(proof => proof.call)
  const initial = await currentNativeAgent(context)
  const start = (await modelScript.status()).stepCount
  await modelScript.queue({ toolCalls: calls }, nativeTextStep(context, 'Both native commands finished.'))
  await sendMessage(page, modelScript.prompt('Run both native Bash calls in one model response.'))
  await waitForNativeToolSteps(context, start + 2)
  const status = await modelScript.status()
  const request = status.requests.find(item => item.stepIndex === start + 1)
  expect(request).toBeDefined()
  if (!request)
    throw new Error('The two native Qoder calls produced no exact next model request.')
  expect(request.mockCredential?.accepted).toBe(true)
  const current = await currentNativeAgent(context)
  expect(current.id).toBe(initial.id)
  expect(current.agentSessionId).toBe(initial.agentSessionId)
  const snapshot = await readNativeMessageSnapshot(context, current.id)
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
    const requestRow = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${call.id}"][data-tool-row-role="request"]:visible`)
    const resultRow = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${call.id}"][data-tool-row-role="result"]:visible`)
    await expect(requestRow).toHaveCount(1)
    await expect(resultRow).toHaveCount(1)
    await expect(requestRow).toContainText(command)
    await expect(resultRow).toContainText(output)
    await expect(resultRow).toHaveAttribute('data-tool-status', 'completed')
  }
})
