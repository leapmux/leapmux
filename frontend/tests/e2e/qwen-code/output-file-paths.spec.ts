import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { QwenOutputPathReceipt } from './outputFilePaths'
import { createHash } from 'node:crypto'
import { unlinkSync } from 'node:fs'
import { isAbsolute, relative } from 'node:path'
import { expect } from '@playwright/test'
import { resolveMessageForRendering } from '../../../src/components/chat/providers/registry'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { openWorkspace, sendMessage } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { qwenTest } from '../qwen-fixtures'
import { qwenChildTurn } from './childScenario'
import { qwenModelOutputPath, qwenOutputPathCommand, qwenOutputPathReceipt } from './outputFilePaths'
import { nativeContext, QWEN_AGENT } from './scenarios'

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
    return frame && acpClosedToolCall(frame, callId) ? [{ message, parsed, frame }] : []
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
  // The shared proof reloads between its two passes and gives no step there, so the files go away before both
  // passes. Each pass, live and after the reload, then proves that the row draws its paths and its preview from the
  // Worker record alone.
  for (const path of expected.receipt.paths)
    unlinkSync(path)
  const readRecord = (snapshot: NativeMessageSnapshot) => {
    const current = resultReceipt(snapshot.messages, expected.receipt.callId, sessionId)
    return { receipt: current.receipt, message: messageProof(current.message) }
  }
  await proveNativeToolOutputFilePaths({
    context,
    callId: expected.receipt.callId,
    previewText: expected.receipt.preview,
    previewMarkers: [generated.lastMarker],
    absentMarkers: [generated.omittedMarker],
    paths: expected.receipt.paths,
    status: expected.receipt.status,
    prepareView: expandNativeResultView,
    workerProof: () => expectUnchangedNativeRecord(context, { id: ownerId, agentSessionId: sessionId }, readRecord, { receipt: expected.receipt, message: messageProof(expected.message) }),
  })
}

for (const exitCode of [0, 7]) {
  qwenTest(exitCode === 0 ? 'keeps native root output paths and the original inline preview after removal and reload' : 'keeps native failed output paths and the original failure preview after removal and reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openProviderAgent(leapmuxServer, context.workspaceId, QWEN_AGENT, { directoryPrefix: 'native-output-path-qwen-' })
    await openWorkspace(page, context.workspaceId)
    const owner = await currentNativeAgent(context)
    const generated = qwenOutputPathCommand(uniqueMarker('NATIVEOUTPUTPATH'), exitCode)
    const callId = `native-output-path-${exitCode}`
    const { resultRequest: request } = await runNativeToolTurn(context, {
      toolCalls: [bashToolCall(context.provider, callId, generated.command)],
      prompt: 'Run the native output path command once.',
      answer: 'The native output path command ended.',
    })
    if (request.mockCredential?.accepted !== true)
      throw new Error('The native Qwen command requires its exact isolated model result.')
    const modelPath = qwenModelOutputPath(nativeToolResult(request, callId))
    const current = await nativeAgentById(context, owner.id)
    if (!current || !current.agentSessionId || current.workingDir !== owner.workingDir
      || (owner.agentSessionId !== '' && current.agentSessionId !== owner.agentSessionId)) {
      throw new Error('The native Qwen command changed its Worker owner.')
    }
    const snapshot = await readNativeMessageSnapshot(context, owner.id)
    const expected = resultReceipt(snapshot.messages, callId, current.agentSessionId)
    expect(expected.receipt.paths).toEqual([modelPath])
    expect(expected.receipt.exitCode).toBe(exitCode)
    expect(expected.receipt.status).toBe(exitCode === 0 ? 'completed' : 'failed')
    await testInfo.attach('qwen-native-output-path-receipt', { body: JSON.stringify({ agentId: owner.id, sessionId: current.agentSessionId, receipt: expected.receipt, original: messageProof(expected.message) }), contentType: 'application/json' })
    await provePathsAndPreview(context, owner.id, current.agentSessionId, expected, generated)
  })
}

for (const background of [false, true]) {
  qwenTest(background ? 'keeps native background child paths and the original preview after removal and reload' : 'keeps native foreground child paths and the original preview after removal and reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openProviderAgent(leapmuxServer, context.workspaceId, QWEN_AGENT, { directoryPrefix: 'native-output-path-qwen-child-', optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    await openWorkspace(page, context.workspaceId)
    const initialRoot = await currentNativeAgent(context)
    const generated = qwenOutputPathCommand(uniqueMarker('NATIVEOUTPUTPATH'))
    const childTask = `CHILD_NATIVE_OUTPUT_PATH_${background ? 'BACKGROUND' : 'FOREGROUND'}`
    const spawnId = background ? 'spawn-native-output-path-background' : 'spawn-native-output-path-foreground'
    const callId = background ? 'native-output-path-background-child' : 'native-output-path-foreground-child'
    const commandRule = 'the native child runs its output path command'
    const finishedRule = 'the native child reports its output path'
    await modelScript.rule(
      { name: commandRule, when: qwenChildTurn(childTask), respond: { toolCalls: [bashToolCall(context.provider, callId, generated.command)] }, once: true },
      { name: finishedRule, when: { body: callId }, respond: { text: 'The native child command ended.' }, once: true },
    )
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(context.provider, spawnId, { description: 'Run the native output path command', prompt: modelScript.prompt(`${childTask}. Run the scripted shell command once.`), background })] },
      { text: background ? 'The native child runs in the background.' : 'The native foreground child ended.' },
      ...(background ? [{ text: 'The native background child ended.' }] : []),
    )
    await sendMessage(page, modelScript.prompt('Delegate the scripted command to a native child.'))
    await modelScript.waitForSteps()
    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    const root = await nativeAgentById(context, initialRoot.id)
    if (!root || !root.agentSessionId || root.workingDir !== initialRoot.workingDir
      || (initialRoot.agentSessionId !== '' && root.agentSessionId !== initialRoot.agentSessionId)) {
      throw new Error('The native child changed its root owner.')
    }
    const childId = await openChildTabFromRow(page, row)
    const child = await nativeAgentById(context, childId)
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
    const snapshot = await readNativeMessageSnapshot(context, childId)
    const expected = resultReceipt(snapshot.messages, callId, root.agentSessionId)
    expect(expected.receipt.paths).toEqual([modelPath])
    expect(expected.receipt.status).toBe('completed')
    await testInfo.attach('qwen-native-child-output-path-receipt', { body: JSON.stringify({ rootId: root.id, childId, parentId: child.parentAgentId, spawnId, sessionId: root.agentSessionId, receipt: expected.receipt, original: messageProof(expected.message) }), contentType: 'application/json' })
    await provePathsAndPreview(context, childId, root.agentSessionId, expected, generated)
  })
}
