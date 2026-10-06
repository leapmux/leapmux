import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { CODEWHALE_AGENT, codewhaleTest, expect } from '../codewhale-fixtures'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { codewhaleBashModelMatches } from './bashModelOutput'
import { codewhaleMcpToolResult } from './mcpToolResult'
import { readCodewhaleNativeOutput } from './outputFilePaths'

codewhaleTest('keeps the native MCP output path and exact preview after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const directory = createTestDirectory('codewhale-native-mcp-output-path-')
  const generated = codewhaleMcpToolResult(computedNativeToolOutput({ lineCount: 8000, padding: 30 }), { approvedByUser: true })
  const receiptLog = join(directory, 'native-mcp-result-receipts.json')
  const server = writeMcpResultServer(directory, { receiptLog, inspectContent: generated.nativeResult.content })
  const home = leapmuxServer.agentEnv.CODEWHALE_HOME
  if (!home)
    throw new Error('The native Codewhale MCP case requires its private home.')
  await withNativeConfigurationFile({ path: join(home, 'mcp.json'), content: JSON.stringify({ servers: { result_probe: { command: process.execPath, args: [server] } } }), runDir: getGlobalState().tmpDir }, async () => {
    await openProviderAgent(leapmuxServer, native.workspaceId, CODEWHALE_AGENT, { workingDir: directory })
    await openWorkspace(page, native.workspaceId)
    await captureNativeToolOutput(native, testInfo, {
      output: generated.capture,
      callId: 'native-output-path',
      call: (_output, callId) => mcpToolCall(native.provider, callId, { server: 'result_probe', tool: 'inspect', input: { count: -1, enabled: false, text: '' } }),
      proof: async (capture) => {
        const receipt = readCodewhaleNativeOutput(capture.snapshot, capture.nativeCallId)
        const path = receipt.paths[0]
        if (!path)
          throw new Error('The native Codewhale MCP result requires a declared path.')
        assertPrivateNativePath(path, getGlobalState().tmpDir)
        expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
        await testInfo.attach('codewhale-native-mcp-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
        const receipts: unknown = JSON.parse(readFileSync(receiptLog, 'utf8'))
        if (!Array.isArray(receipts))
          throw new Error('The native Codewhale MCP case returned no receipt array.')
        const calls = receipts.filter(isObject).map(receipt => receipt.request).filter(isObject).filter(request => request.method === 'tools/call')
        expect(calls).toHaveLength(1)
        expect(calls[0]?.params).toMatchObject({ name: 'inspect', arguments: { count: -1, enabled: false, text: '' } })
        const params = isObject(calls[0]?.params) ? calls[0].params : undefined
        expect(params?.arguments).toEqual({ count: -1, enabled: false, text: '' })
        const replies = receipts.filter(isObject).map(receipt => receipt.reply).filter(isObject).filter(reply => reply.id === calls[0]?.id)
        expect(replies).toHaveLength(1)
        expect(replies[0]?.result).toEqual(generated.nativeResult)
        await proveNativeToolOutputFilePaths({
          context: native,
          callId: capture.nativeCallId,
          previewText: receipt.previewText,
          previewMarkers: [capture.output.firstMarker, capture.output.lastMarker],
          paths: receipt.paths,
          status: 'completed',
          prepareView: expandNativeResultView,
          workerProof: async () => {
            const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
            expect(snapshot.agentSessionId).toBe(capture.agent.agentSessionId)
            const current = readCodewhaleNativeOutput(snapshot, capture.nativeCallId)
            expect(current.frame).toEqual(receipt.frame)
            expect(current.content).toEqual(receipt.content)
            expect(current.paths).toEqual(receipt.paths)
            expect(current.previewText).toBe(receipt.previewText)
          },
        })
      },
    })
  })
})

codewhaleTest('keeps the native Bash output path and limit after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await openProviderAgent(leapmuxServer, native.workspaceId, CODEWHALE_AGENT, { workingDir: createTestDirectory('codewhale-native-bash-cap-') })
  await openWorkspace(page, native.workspaceId)
  await captureNativeToolOutput(native, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-bash-first-cap',
    proof: async (capture) => {
      const receipt = readCodewhaleNativeOutput(capture.snapshot, capture.nativeCallId)
      expect(receipt.paths).toHaveLength(1)
      const path = receipt.paths[0]
      if (!path)
        throw new Error('The native result requires its declared path.')
      assertPrivateNativePath(path, getGlobalState().tmpDir)
      expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
      const previewMarkers = [capture.output.firstMarker, capture.output.lastMarker].filter(marker => receipt.previewText.includes(marker))
      expect(previewMarkers.length).toBeGreaterThan(0)
      const item = isObject(receipt.frame.payload) ? receipt.frame.payload.item : undefined
      expect(isObject(item) && isObject(item.metadata) ? item.metadata.artifact_id : undefined).toBeUndefined()
      const excerpt = nativeToolResult(capture.request, capture.nativeCallId)
      expect(codewhaleBashModelMatches(receipt.previewText, excerpt)).toBe(true)
      await testInfo.attach('native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers,
        paths: receipt.paths,
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
          expect(snapshot.agentSessionId).toBe(capture.agent.agentSessionId)
          const current = readCodewhaleNativeOutput(snapshot, capture.nativeCallId)
          expect(current.frame).toEqual(receipt.frame)
          expect(current.content).toEqual(receipt.content)
          expect(current.paths).toEqual(receipt.paths)
          expect(current.previewText).toBe(receipt.previewText)
        },
      })
    },
  })
})
