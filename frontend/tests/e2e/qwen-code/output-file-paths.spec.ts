import type { Locator, Page } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { QwenOutputPathReceipt } from './outputFilePaths'
import { createHash, randomUUID } from 'node:crypto'
import { unlinkSync } from 'node:fs'
import { isAbsolute, relative } from 'node:path'
import { resolveMessageForRendering } from '../../../src/components/chat/providers/registry'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent, nativeAgentById, nativeTextStep } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { nativeOutputPathsPrecedePreview } from '../helpers/nativeToolOutputFilePaths'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { openWorkspace, readAttachedWithArgument, sendMessage } from '../helpers/ui'
import { expect, qwenTest } from '../qwen-fixtures'
import { qwenModelOutputPath, qwenOutputPathCommand, qwenOutputPathReceipt } from './outputFilePaths'

function messageProof(message: AgentChatMessage) {
  return {
    id: message.id,
    seq: String(message.seq),
    sessionId: message.agentSessionId,
    spanId: message.spanId,
    compression: message.contentCompression,
    bytes: message.content.byteLength,
    sha256: createHash('sha256').update(message.content).digest('hex'),
    supplementCompression: message.supplementalContentCompression,
    supplementBytes: message.supplementalContent.byteLength,
    supplementSHA256: createHash('sha256').update(message.supplementalContent).digest('hex'),
  }
}

function resultReceipt(messages: readonly AgentChatMessage[], callId: string, sessionId: string) {
  const results = messages.filter(message => message.spanId === callId && message.agentSessionId === sessionId).flatMap((message) => {
    const parsed = parseMessageContent(message)
    const frame = resolveMessageForRendering(parsed, AgentProvider.QWEN_CODE).parentObject
    return frame?.sessionUpdate === 'tool_call_update' && frame.toolCallId === callId && (frame.status === 'completed' || frame.status === 'failed')
      ? [{ message, parsed, frame }]
      : []
  })
  expect(results).toHaveLength(1)
  const result = results[0]
  if (!result)
    throw new Error('The native Qwen call requires one exact completed Worker result.')
  const supplement = isObject(result.parsed.supplementalContent) ? result.parsed.supplementalContent : {}
  const raw = isObject(supplement.rawOutput) ? supplement.rawOutput : {}
  expect(supplement).not.toHaveProperty('qwenOutputFile')
  expect(raw).not.toHaveProperty('qwenToolRecord')
  expect(raw).not.toHaveProperty('qwenAgentMeta')
  return { receipt: qwenOutputPathReceipt(result.frame), message: result.message }
}

async function resultView(page: Page, callId: string): Promise<Locator> {
  const escaped = await page.evaluate(id => CSS.escape(id), callId)
  const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id=${escaped}][data-tool-row-role="result"]:visible`)
  await expect(result).toHaveCount(1)
  return result
}

async function provePathsAndPreview(
  context: ManagedNativeScenarioContext,
  ownerId: string,
  sessionId: string,
  expected: { receipt: QwenOutputPathReceipt, message: AgentChatMessage },
  generated: { omittedMarker: string, lastMarker: string },
): Promise<void> {
  const agentEnv = context.leapmuxServer.agentEnv
  const runtime = agentEnv?.QWEN_RUNTIME_DIR || agentEnv?.QWEN_HOME
  const home = agentEnv?.HOME
  if (!runtime || !home)
    throw new Error('The native Qwen path proof requires the isolated runtime and home.')
  assertPrivateNativePath(runtime, home)
  for (const path of expected.receipt.paths) {
    assertPrivateNativePath(path, runtime)
    const descendant = relative(runtime, path)
    if (!isAbsolute(path) || descendant === '' || descendant.startsWith('..') || isAbsolute(descendant))
      throw new Error('The native Qwen output path must stay inside its private runtime.')
  }
  expect(expected.receipt.preview.includes(generated.omittedMarker)).toBe(false)
  // The native preview keeps the tail. The computed last line occurs only in returned output.
  expect(expected.receipt.preview.includes(generated.lastMarker)).toBe(true)
  for (const reloaded of [false, true]) {
    if (reloaded) {
      for (const path of expected.receipt.paths)
        unlinkSync(path)
      await context.page.reload()
      await openWorkspace(context.page, context.workspaceId)
    }
    const snapshot = await readNativeMessageSnapshot(context, ownerId)
    const current = resultReceipt(snapshot.messages, expected.receipt.callId, sessionId)
    expect(current.receipt).toEqual(expected.receipt)
    expect(messageProof(current.message)).toEqual(messageProof(expected.message))
    const result = await resultView(context.page, expected.receipt.callId)
    await expect(result).toHaveAttribute('data-tool-status', expected.receipt.status)
    const paths = result.getByTestId('tool-output-file-paths')
    await expect(paths).toBeVisible()
    expect(await paths.textContent()).toBe(expected.receipt.paths.map(path => `Output file:${path}`).join(''))
    await expect(paths.getByRole('link')).toHaveCount(0)
    await expandNativeResultView(result)
    expect(await readAttachedWithArgument(result, 'native output property order', nativeOutputPathsPrecedePreview, [generated.lastMarker])).toBe(true)
    await copyNativeToolOutputPreview(context.page, result, expected.receipt.preview)
  }
}

for (const exitCode of [0, 7]) {
  qwenTest(exitCode === 0 ? 'keeps native root output paths and the original inline preview after removal and reload' : 'keeps native failed output paths and the original failure preview after removal and reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, native.workspaceId, createTestDirectory('native-output-path-qwen-'), { agentProvider: native.provider, ...agentOpenOptions(agentSettings(native.provider)) })
    await openWorkspace(page, native.workspaceId)
    const owner = await currentNativeAgent(native)
    const generated = qwenOutputPathCommand(`NATIVEOUTPUTPATH${randomUUID().replaceAll('-', '')}`, exitCode)
    const callId = `native-output-path-${exitCode}`
    const step = (await modelScript.status()).stepCount
    await modelScript.queue({ toolCalls: [bashToolCall(native.provider, callId, generated.command)] }, nativeTextStep(native, 'The native output path command ended.'))
    await sendMessage(page, modelScript.prompt('Run the native output path command once.'))
    await waitForNativeToolSteps(native, step + 2)
    const status = await modelScript.status()
    const request = status.requests.find(item => item.stepIndex === step + 1)
    if (!request || request.mockCredential?.accepted !== true)
      throw new Error('The native Qwen command requires its exact isolated model result.')
    const modelPath = qwenModelOutputPath(nativeToolResult(request, callId))
    const current = await nativeAgentById(native, owner.id)
    if (!current || !current.agentSessionId || current.workingDir !== owner.workingDir
      || (owner.agentSessionId !== '' && current.agentSessionId !== owner.agentSessionId)) {
      throw new Error('The native Qwen command changed its Worker owner.')
    }
    const snapshot = await readNativeMessageSnapshot(native, owner.id)
    const expected = resultReceipt(snapshot.messages, callId, current.agentSessionId)
    expect(expected.receipt.paths).toEqual([modelPath])
    expect(expected.receipt.exitCode).toBe(exitCode)
    expect(expected.receipt.status).toBe(exitCode === 0 ? 'completed' : 'failed')
    await testInfo.attach('qwen-native-output-path-receipt', { body: JSON.stringify({ agentId: owner.id, sessionId: current.agentSessionId, receipt: expected.receipt, original: messageProof(expected.message) }), contentType: 'application/json' })
    await provePathsAndPreview(native, owner.id, current.agentSessionId, expected, generated)
  })
}

for (const background of [false, true]) {
  qwenTest(background ? 'keeps native background child paths and the original preview after removal and reload' : 'keeps native foreground child paths and the original preview after removal and reload', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, native.workspaceId, createTestDirectory('native-output-path-qwen-child-'), { agentProvider: native.provider, ...agentOpenOptions(agentSettings(native.provider)), optionValues: { ...agentOpenOptions(agentSettings(native.provider)).optionValues, [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    await openWorkspace(page, native.workspaceId)
    const initialRoot = await currentNativeAgent(native)
    const generated = qwenOutputPathCommand(`NATIVEOUTPUTPATH${randomUUID().replaceAll('-', '')}`)
    const childTask = `CHILD_NATIVE_OUTPUT_PATH_${background ? 'BACKGROUND' : 'FOREGROUND'}`
    const spawnId = background ? 'spawn-native-output-path-background' : 'spawn-native-output-path-foreground'
    const callId = background ? 'native-output-path-background-child' : 'native-output-path-foreground-child'
    const commandRule = 'the native child runs its output path command'
    const finishedRule = 'the native child reports its output path'
    await modelScript.rule(
      { name: commandRule, when: { user: childTask }, respond: { toolCalls: [bashToolCall(native.provider, callId, generated.command)] }, once: true },
      { name: finishedRule, when: { body: callId }, respond: { text: 'The native child command ended.' }, once: true },
    )
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(native.provider, spawnId, { description: 'Run the native output path command', prompt: modelScript.prompt(`${childTask}. Run the scripted shell command once.`), background })] },
      { text: background ? 'The native child runs in the background.' : 'The native foreground child ended.' },
      ...(background ? [{ text: 'The native background child ended.' }] : []),
    )
    await sendMessage(page, modelScript.prompt('Delegate the scripted command to a native child.'))
    await modelScript.waitForSteps()
    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    const root = await nativeAgentById(native, initialRoot.id)
    if (!root || !root.agentSessionId || root.workingDir !== initialRoot.workingDir
      || (initialRoot.agentSessionId !== '' && root.agentSessionId !== initialRoot.agentSessionId)) {
      throw new Error('The native child changed its root owner.')
    }
    const childId = await openChildTabFromRow(page, row)
    const child = await nativeAgentById(native, childId)
    if (!child || child.parentAgentId !== root.id || child.rootAgentId !== root.id || child.spawnSpanId !== spawnId || child.agentSessionId !== root.agentSessionId)
      throw new Error('The native Qwen child has another parent, session, or spawn.')
    const status = await modelScript.status()
    expect(status.ruleMatches[commandRule]).toBe(1)
    expect(status.ruleMatches[finishedRule]).toBe(1)
    const requests = status.requests.filter(item => item.rule === finishedRule)
    expect(requests).toHaveLength(1)
    const request = requests[0]
    if (!request || request.mockCredential?.accepted !== true)
      throw new Error('The native child requires its exact isolated model result.')
    const modelPath = qwenModelOutputPath(nativeToolResult(request, callId))
    const snapshot = await readNativeMessageSnapshot(native, childId)
    const expected = resultReceipt(snapshot.messages, callId, root.agentSessionId)
    expect(expected.receipt.paths).toEqual([modelPath])
    expect(expected.receipt.status).toBe('completed')
    await testInfo.attach('qwen-native-child-output-path-receipt', { body: JSON.stringify({ rootId: root.id, childId, parentId: child.parentAgentId, spawnId, sessionId: root.agentSessionId, receipt: expected.receipt, original: messageProof(expected.message) }), contentType: 'application/json' })
    await provePathsAndPreview(native, childId, root.agentSessionId, expected, generated)
  })
}
