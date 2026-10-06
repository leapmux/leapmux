import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { cursorTest, expect } from '../cursor-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatScrollContainer, openWorkspace } from '../helpers/ui'
import { cursorNativeToolOutput, runCursorNativeOperations } from './nativeExecutionScenario'

cursorTest('retains complete inline output for the selected native shell route after reload', async ({ authenticatedCursorWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const callId = 'cursor-native-inline-output'
  const command = nativeOutputFileCommand(output)
  await runCursorNativeOperations(native, [bashToolCall(native.provider, callId, command)], output.lastMarker)
  const agent = await currentNativeAgent(native)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  const exact = await cursorNativeToolOutput(native, callId)
  await testInfo.attach('cursor-native-inline-output-records', { body: JSON.stringify({ callId, sessionId: agent.agentSessionId, previewText: output.text, exact, messages: snapshot.messages.map(nativeMessageBody), modelStatus: await modelScript.status() }, null, 2), contentType: 'application/json' })
  if (!isObject(exact) || exact.exitCode !== 0 || typeof exact.stdout !== 'string' || exact.stderr !== '')
    throw new Error('The selected native Cursor shell route did not return its complete successful output.')
  const text = exact.stdout
  const bytes = Buffer.from(text, 'utf8')
  await testInfo.attach('cursor-native-inline-output', { body: text, contentType: 'text/plain' })
  await testInfo.attach('cursor-native-output-limit-proof', { body: JSON.stringify({ route: 'native-shell', callId, sessionId: agent.agentSessionId, byteSize: bytes.length, digest: createHash('sha256').update(bytes).digest('hex'), nativeReference: exact }, null, 2), contentType: 'application/json' })
  expect(text).toBe(output.text)
  expect(exact).not.toHaveProperty('fullOutputPath')
  const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`)
  const originals = snapshot.messages.filter(message => message.spanId === callId).map(nativeMessageBody)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, native.workspaceId)
    }
    const current = await currentNativeAgent(native)
    expect(current.id).toBe(agent.id)
    expect(current.agentSessionId).toBe(agent.agentSessionId)
    const stored = await readNativeMessageSnapshot(native, current.id)
    expect(stored.messages.filter(message => message.spanId === callId).map(nativeMessageBody)).toEqual(originals)
    expect(await cursorNativeToolOutput(native, callId)).toEqual(exact)
    await chatScrollContainer(page).evaluate(element => element.scrollTo({ top: 0, behavior: 'instant' }))
    await expect(result).toHaveCount(1)
    await expandNativeResultView(result)
    await expect(result).toContainText(output.firstMarker)
    await expect(result).toContainText(output.lastMarker)
    await expect(result).not.toContainText(output.omittedMarker)
    await expect(result.getByTestId('tool-output-file-paths')).toHaveCount(0)
    await copyNativeToolOutputPreview(page, result, output.text)
  }
})
