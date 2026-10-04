import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest, expect } from '../amp-fixtures'
import { ampToolUseID } from '../helpers/ampSurface'
import { ampToolResult } from '../helpers/ampToolResult'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { openWorkspace } from '../helpers/ui'
import { ampNativeOutputLimit } from './nativeToolOutput'

ampTest('records the native large shell output limit and retains its exact tail after reload', async ({ authenticatedAmpWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const before = await currentNativeAgent(native)
  if (!before.workingDir)
    throw new Error('The Amp native output proof requires a native working directory.')
  const sourceFile = join(before.workingDir, 'amp-controlled-complete-output.txt')
  assertPrivateNativePath(before.workingDir, getGlobalState().tmpDir)
  expect(existsSync(sourceFile)).toBe(false)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'amp-native-output-limit',
    call: (sourceOutput, callId) => {
      const source = `${sourceOutput.source} require("node:fs").writeFileSync(${JSON.stringify(sourceFile)}, completeOutput, { flag: "wx" }); process.stdout.write(completeOutput)`
      return bashToolCall(native.provider, callId, `node -e ${quotePosixShellArgument(source)}`)
    },
    proof: async (capture) => {
      const nativeId = ampToolUseID(capture.agent.agentSessionId, capture.call.id)
      expect(capture.agent.id).toBe(before.id)
      expect(capture.agent.workingDir).toBe(before.workingDir)
      expect(dirname(sourceFile)).toBe(capture.agent.workingDir)
      expect(existsSync(sourceFile)).toBe(true)
      assertPrivateNativePath(sourceFile, getGlobalState().tmpDir)
      const exact = ampToolResult(capture.request, nativeId)
      expect(exact.exitCode).toBe(0)
      const retained = ampNativeOutputLimit(exact.text, output.text)
      await testInfo.attach('amp-native-output-limit', { body: JSON.stringify({ nativeId, sessionId: capture.agent.agentSessionId, sourceFile, previewText: retained.output, retained }, null, 2), contentType: 'application/json' })
      if ('prefixLinesOmitted' in retained)
        expect(retained.prefixLinesOmitted).toBeGreaterThan(0)
      else
        expect(retained.exitCode).toBe(0)
      expect(retained.output).toContain(output.lastMarker)
      expect(retained.output).not.toContain(output.firstMarker)
      expect(retained.output).not.toContain(output.omittedMarker)
      expect(retained.output).not.toContain(output.text)
      const originals = capture.snapshot.messages.filter(message => message.spanId === nativeId).map(nativeMessageBody)
      expect(originals).not.toHaveLength(0)
      const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${nativeId}"][data-tool-row-role="result"]:visible`)
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await openWorkspace(page, native.workspaceId)
        }
        const current = await currentNativeAgent(native)
        expect(current.id).toBe(capture.agent.id)
        expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
        const snapshot = await readNativeMessageSnapshot(native, current.id)
        expect(snapshot.messages.filter(message => message.spanId === nativeId).map(nativeMessageBody)).toEqual(originals)
        await expect(result).toHaveCount(1)
        await expandNativeResultView(result)
        await expect(result).toContainText(output.lastMarker)
        await expect(result).not.toContainText(output.firstMarker)
        await expect(result).not.toContainText(output.omittedMarker)
        await expect(result.getByTestId('tool-output-file-paths')).toHaveCount(0)
        await copyNativeToolOutputPreview(page, result, retained.output)
      }
    },
  })
})
