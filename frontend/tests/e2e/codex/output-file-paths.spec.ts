import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEX_AGENT, codexTest } from '../codex-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { codexNativeOutputExcerpt } from './nativeToolOutput'

codexTest('preserves the native exec output limit and copies only its retained excerpt after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEX }
  await openProviderAgent(leapmuxServer, native.workspaceId, CODEX_AGENT, { workingDir: createTestDirectory('codex-native-output-limit-') })
  await openWorkspace(page, native.workspaceId)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const callId = 'native-exec-native-output-limit'
  const source = `// @exec: {"max_output_tokens":300}\n${output.source}\ntext(completeOutput);`
  await modelScript.queue({ toolCalls: [codexExecToolCall(callId, source)] }, { text: 'The native exec excerpt completed.' })
  await sendMessage(page, modelScript.prompt('Run the native exec large output once.'))
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const agent = await currentNativeAgent(native)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  const excerpt = nativeToolResult(status.requests.find(request => request.stepIndex === 1), callId)
  await testInfo.attach('codex-native-output-limit', { body: JSON.stringify({ callId, sessionId: agent.agentSessionId, excerpt, messages: snapshot.messages.map(nativeMessageBody), requests: status.requests }, null, 2), contentType: 'application/json' })
  expect(excerpt).toContain('Warning: truncated output')
  expect(excerpt).toContain(output.firstMarker)
  expect(excerpt).toContain(output.lastMarker)
  expect(excerpt).not.toContain(output.omittedMarker)
  const retainedText = codexNativeOutputExcerpt(excerpt)
  const originals = snapshot.messages.filter(message => message.spanId === callId)
  const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, native.workspaceId)
    }
    const current = await currentNativeAgent(native)
    expect(current.id).toBe(agent.id)
    expect(current.agentSessionId).toBe(agent.agentSessionId)
    const stored = await readNativeMessageSnapshot(native, current.id)
    expect(stored.messages.filter(message => message.spanId === callId)).toEqual(originals)
    await expect(result).toHaveCount(1)
    await expandNativeResultView(result)
    await expect(result).toContainText(output.firstMarker)
    await expect(result).toContainText(output.lastMarker)
    await expect(result).not.toContainText(output.omittedMarker)
    await expect(result.getByTestId('tool-output-file-paths')).toHaveCount(0)
    await copyNativeToolOutputPreview(page, result, retainedText)
  }
})
