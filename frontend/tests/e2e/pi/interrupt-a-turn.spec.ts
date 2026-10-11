import type { ServerInfo } from '../fixtures'
import type { PiStopRelayOptions } from './stopRelay'
import { Buffer } from 'node:buffer'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { PI_EVENT } from '../../../src/generated/contracts/pi-protocol'
import { AgentProvider, InterruptAgentRequestSchema, InterruptAgentResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel, openAgentViaAPI } from '../helpers/api'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeStartupShellEnvironment } from '../helpers/nativeStartupWorker'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { withNativeWorker } from '../helpers/nativeWorker'
import { piEditorProbeToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { uniqueMarker } from '../helpers/shellArguments'
import { assistantBubbles, controlBanner, controlButton, expectNoControlBanner, expectSettingsChip, messageBubbles, openWorkspace, resumePausedQueue, savedControlAnswer, sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { PI_AGENT } from './scenarios'
import { withMockPiModel } from './scriptedModel'
import { createPiStopRelay, withPiRetrySignal } from './stopRelay'

piTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

piTest('stops an actual native tool and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

piTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})

// Pi's question extension asks through a native dialog. The interrupt must release that dialog.
piTest('withdraws a waiting question and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})

/** Start an isolated Worker whose Pi launcher records and orders actual native bytes. */
async function withStopRelayWorker(
  server: ServerInfo,
  options: Omit<PiStopRelayOptions, 'executable' | 'args'>,
  use: (server: ServerInfo) => Promise<void>,
): Promise<void> {
  const wrapper = createTestDirectory('pi-stop-relay-')
  const shell = createTestDirectory('pi-stop-relay-shell-')
  const launch = resolveNativeStartupLaunch(server.agentEnv, { binaryName: 'pi' })
  createPiStopRelay(wrapper, { ...options, executable: launch.executable, args: launch.args ?? [] })
  const environment = nativeStartupShellEnvironment(shell, wrapper, hubSpawnEnv(server.agentEnv))
  await withNativeWorker(server, {
    dataDirPrefix: 'pi-stop-relay-worker',
    workerName: 'Pi stop ownership proof',
    env: environment,
    privateDirectories: [wrapper, shell],
  }, worker => use(worker.server))
}

/** Decode the evidence copy. The relay forwards each native line with its original bytes. */
function relayEvidence(path: string): { kind: string, bytes: string, frame: Record<string, unknown> }[] {
  return readFileSync(path, 'utf8').trim().split('\n').map((line) => {
    const record = JSON.parse(line) as { kind: string, bytes: string }
    let frame: Record<string, unknown> = {}
    try {
      const decoded: unknown = JSON.parse(Buffer.from(record.bytes, 'base64').toString())
      if (decoded !== null && typeof decoded === 'object' && !Array.isArray(decoded))
        frame = decoded as Record<string, unknown>
    }
    catch {
      // Diagnostics can hold text. Preserve their bytes without a protocol interpretation.
    }
    return { ...record, frame }
  })
}

piTest('retains an answerable native replacement question after the old abort succeeds', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
  const marker = uniqueMarker('REPLACEMENT')
  const directory = newProviderWorkingDir(PI_AGENT, 'pi-stop-replacement-')
  const evidencePath = join(directory, 'relay-evidence.jsonl')
  const receipt = join(directory, 'replacement-receipt.json')
  const title = `Replacement question ${marker}`
  const answer = `Replacement answer ${marker}`
  const gate = `original-turn-${marker}`
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    writeFileSync(join(directory, '.pi', 'extensions', 'editor-probe.ts'), `
import { writeFileSync } from 'node:fs';
export default function (pi) {
  pi.registerTool({
    name: 'editor_probe', label: 'Editor probe', description: 'Ask the replacement question.',
    parameters: { type: 'object', properties: {}, required: [] },
    async execute(_id, _params, _signal, _update, ctx) {
      const value = await ctx.ui.editor(${JSON.stringify(title)}, 'Replacement prefill');
      writeFileSync(${JSON.stringify(receipt)}, JSON.stringify({ value, cancelled: value === undefined }));
      return { content: [{ type: 'text', text: 'REPLACEMENT_RESPONSE_RECEIVED' }], details: {} };
    },
  });
}
`)
    await withStopRelayWorker(leapmuxServer, {
      originalMarker: marker,
      replacement: { prompt: modelScript.prompt(`Run the editor probe in replacement turn ${marker}.`), title },
      evidencePath,
    }, async (server) => {
      const agentId = await openAgentViaAPI(server, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await expectSettingsChip(page, 'Protocol test')
      const channel = await getTestChannel(server.hubUrl, server.adminToken)
      await channel.getOrOpenChannel(server.workerId)
      const start = await modelScript.queue(
        { gate, text: 'The original turn must stop.' },
        { toolCalls: [piEditorProbeToolCall(`replacement-editor-${marker}`)] },
        { text: `Replacement complete ${marker}.` },
      )
      try {
        await sendMessage(page, modelScript.prompt(`Hold original turn ${marker}.`))
        await modelScript.waitForGate(gate)
        await channel.callWorker(server.workerId, 'InterruptAgent', InterruptAgentRequestSchema, InterruptAgentResponseSchema, { agentId })
        const records = relayEvidence(evidencePath)
        const held = records.find(record => record.kind === 'held-abort-reply')
        const released = records.find(record => record.kind === 'released-abort-reply')
        const dialogIndex = records.findIndex(record => record.kind === 'replacement-dialog')
        const releaseIndex = records.findIndex(record => record.kind === 'released-abort-reply')
        if (!held || !released)
          throw new Error('The relay must record both stages of the actual abort acknowledgement.')
        expect(held.frame).toMatchObject({ type: PI_EVENT.Response, command: 'abort', success: true })
        expect(released.bytes).toBe(held.bytes)
        expect(dialogIndex).toBeGreaterThan(-1)
        expect(releaseIndex).toBeGreaterThan(dialogIndex)
        const banner = controlBanner(page)
        await expect(banner).toContainText(title)
        const editor = banner.getByTestId('dialog-editor')
        await expect(editor).toHaveValue('Replacement prefill')
        await page.reload()
        await expect(banner).toContainText(title)
        await expect(editor).toHaveValue('Replacement prefill')
        await editor.fill(answer)
        await controlButton(page, 'allow').click()
        await expectNoControlBanner(page)
        await modelScript.waitForSteps(start + 3)
        await waitForAgentIdle(page)
        expect(JSON.parse(readFileSync(receipt, 'utf8'))).toEqual({ value: answer, cancelled: false })
        await expect(savedControlAnswer(page)).toHaveText(answer)
        await expect(messageBubbles(page).filter({ hasText: 'REPLACEMENT_RESPONSE_RECEIVED' }).first()).toBeVisible()
        await expect(assistantBubbles(page).filter({ hasText: `Replacement complete ${marker}.` }).first()).toBeVisible()
        await resumePausedQueue(page)
      }
      finally {
        await modelScript.releaseGateIfHeld(gate)
        await testInfo.attach('pi-replacement-native-bytes', { path: evidencePath, contentType: 'application/x-ndjson' })
      }
    })
  })
})

piTest('settles a cancelled native retry and completes a later prompt in the same process', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
  const marker = uniqueMarker('BACKOFF')
  const directory = newProviderWorkingDir(PI_AGENT, 'pi-stop-backoff-')
  const evidencePath = join(directory, 'relay-evidence.jsonl')
  let retryBytes: Buffer | undefined
  let interrupt: (() => Promise<unknown>) | undefined
  let resolveStop!: () => void
  let rejectStop!: (error: unknown) => void
  const stopped = new Promise<void>((resolve, reject) => {
    resolveStop = resolve
    rejectStop = reject
  })
  // Attach the failure handler before the native signal can arrive.
  stopped.catch(() => {})
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await withPiRetrySignal((bytes) => {
      retryBytes = bytes
      if (!interrupt) {
        rejectStop(new Error('The Worker interrupt call must exist before the native retry starts.'))
        return
      }
      void interrupt().then(resolveStop, rejectStop)
    }, retrySignal => withStopRelayWorker(leapmuxServer, { originalMarker: marker, evidencePath, retrySignal }, async (server) => {
      const agentId = await openAgentViaAPI(server, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
      const channel = await getTestChannel(server.hubUrl, server.adminToken)
      await channel.getOrOpenChannel(server.workerId)
      interrupt = () => channel.callWorker(server.workerId, 'InterruptAgent', InterruptAgentRequestSchema, InterruptAgentResponseSchema, { agentId })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await expectSettingsChip(page, 'Protocol test')
      const completion = `After cancelled retry ${marker}.`
      const start = await modelScript.queue(
        { error: { status: 400, code: 'invalid_request_error', message: `500 native retry ${marker}` } },
        { text: completion },
      )
      try {
        await sendMessage(page, modelScript.prompt(`Start the scripted native backoff ${marker}.`))
        await stopped
        if (!retryBytes)
          throw new Error('The native retry start did not supply its original bytes.')
        expect(JSON.parse(retryBytes.toString())).toMatchObject({ type: PI_EVENT.AutoRetryStart, attempt: 1, maxAttempts: 3, delayMs: 2000 })
        await expect.poll(() => relayEvidence(evidencePath).filter(record => record.kind === 'native-output' && record.frame.type === PI_EVENT.AgentSettled)).toHaveLength(1)
        const records = relayEvidence(evidencePath)
        const cancelled = records.find(record => record.kind === 'native-output' && record.frame.type === PI_EVENT.AutoRetryEnd)
        expect(cancelled?.frame).toMatchObject({ success: false, attempt: 1, finalError: 'Retry cancelled' })
        expect(records.some(record => record.kind === 'native-output' && record.frame.type === PI_EVENT.AgentEnd && record.frame.willRetry === true)).toBe(true)
        await expect(page.locator('[data-testid="result-divider"]:visible').filter({ hasText: 'auto-retry' }).first()).toContainText(`500 native retry ${marker}`)
        await expect(visibleOnly(page.getByText('Retry cancelled', { exact: false })).first()).toBeVisible()
        await resumePausedQueue(page)
        await sendMessage(page, modelScript.prompt(`Complete a normal turn after cancelled retry ${marker}.`))
        await modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(page)
        await expect(assistantBubbles(page).filter({ hasText: completion }).first()).toBeVisible()
        const finalRecords = relayEvidence(evidencePath)
        const starts = finalRecords.filter(record => record.kind === 'native-output' && record.frame.type === PI_EVENT.AgentStart)
        expect(starts).toHaveLength(2)
        const processes = finalRecords.filter(record => record.kind === 'native-process' && Array.isArray(record.frame.argv) && record.frame.argv.includes('rpc'))
        expect(processes).toHaveLength(1)
        expect(processes[0]?.frame.pid).toEqual(expect.any(Number))
      }
      finally {
        await testInfo.attach('pi-backoff-native-bytes', { path: evidencePath, contentType: 'application/x-ndjson' })
      }
    }))
  })
})
