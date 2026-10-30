import type { ServerInfo } from '../fixtures'
import type { NativeWorker } from '../helpers/nativeWorker'
import { Buffer } from 'node:buffer'
import { existsSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { ZCODE_EVENT, ZCODE_METHOD } from '../../../src/generated/contracts/zcode-protocol'
import { AgentProvider, MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { openAgentViaAPI } from '../helpers/api'
import { findBinary, requireBinary } from '../helpers/binaryOnPath'
import { expectNoUnrenderedRow, sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForNativeInputQueueIdle } from '../helpers/nativeInputQueueIdle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { nativeErrorMarker } from '../helpers/nativeModelError'
import { nativeStartupShellEnvironment } from '../helpers/nativeStartupWorker'
import { withNativeWorker } from '../helpers/nativeWorker'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { uniqueMarker } from '../helpers/shellArguments'
import { currentTestDeadline } from '../helpers/testDeadline'
import { assistantBubbles, openWorkspace, sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { zcodeScriptCandidatePaths } from '../zcode-install'
import { writeZcodeRecoveryRelay } from './recoveryRelay'
import { nativeContext, ZCODE_AGENT } from './scenarios'

interface RelayEvidence { direction: 'worker' | 'native', raw: string, forwarded: string | null, fragment: boolean }
function readEvidence(path: string): RelayEvidence[] {
  return readFileSync(path, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line) as RelayEvidence)
}
function decodeFrame(value: string): unknown {
  return JSON.parse(Buffer.from(value, 'base64').toString('utf8'))
}
async function withRecoveryWorker(server: ServerInfo, marker: string, use: (worker: NativeWorker<ServerInfo>, path: string) => Promise<void>): Promise<void> {
  const directory = createTestDirectory('zcode-recovery-relay-')
  const shell = createTestDirectory('zcode-recovery-shell-')
  const evidencePath = join(directory, 'evidence.jsonl')
  const environment = hubSpawnEnv(server.agentEnv)
  const launcher = findBinary('zcode', environment)
  let workerEnvironment: NodeJS.ProcessEnv
  if (launcher && !process.env.LEAPMUX_ZCODE_SCRIPT) {
    writeZcodeRecoveryRelay(directory, {
      launch: { binaryName: 'zcode', executable: launcher },
      marker,
      evidencePath,
    })
    workerEnvironment = { LEAPMUX_ZCODE_SCRIPT: '', LEAPMUX_ZCODE_NODE: '' }
  }
  else {
    const script = process.env.LEAPMUX_ZCODE_SCRIPT ?? zcodeScriptCandidatePaths(process.platform, homedir(), process.env).find(existsSync)
    if (!script)
      throw new Error('The recovery scenario requires the installed ZCode script.')
    writeZcodeRecoveryRelay(directory, {
      launch: {
        binaryName: 'node',
        executable: requireBinary('node', 'The recovery scenario requires Node', environment),
      },
      marker,
      evidencePath,
    })
    workerEnvironment = {
      LEAPMUX_ZCODE_SCRIPT: script,
      LEAPMUX_ZCODE_NODE: join(directory, process.platform === 'win32'
        ? 'node.cmd'
        : 'node'),
    }
  }
  await withNativeWorker(server, {
    dataDirPrefix: 'zcode-recovery-worker',
    workerName: 'ZCode recovery evidence',
    env: {
      ...nativeStartupShellEnvironment(shell, directory, environment),
      ...workerEnvironment,
    },
    privateDirectories: [directory, shell],
  }, worker => use(worker, evidencePath))
}

// The relay presents a consistent native log with one omitted end. A dropped live
// packet alone would be restored by replay and would not exercise reconciliation.
for (const failed of [false, true]) {
  zcodeTest(failed ? 'renders a recovered native failure and completes the next turn' : 'renders a recovered native completion and completes the next turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    const marker = uniqueMarker('RECOVERY')
    const workingDir = newProviderWorkingDir(ZCODE_AGENT, 'zcode-recovery-')
    await withRecoveryWorker(leapmuxServer, marker, async (worker, evidencePath) => {
      const context = await nativeContext({
        page,
        leapmuxServer: worker.server,
        modelScript,
        workspaceId: authenticatedEmptyWorkspace.workspaceId,
      })
      const agentId = await openAgentViaAPI(worker.server, authenticatedEmptyWorkspace.workspaceId, workingDir, { agentProvider: AgentProvider.ZCODE })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const answer = `Recovered answer ${marker}`
      const step = await modelScript.queue(failed
        ? {
            error: {
              status: 400,
              code: 'invalid_request_error',
              message: nativeErrorMarker(),
            },
          }
        : { text: answer })
      try {
        await sendMessage(page, modelScript.prompt(`Reply once for ${marker}.`))
        await modelScript.waitForSteps(step + 1)
        await retryUntilPass(() => {
          expect(readEvidence(evidencePath).filter(record => record.direction === 'native' && record.forwarded === null)).toHaveLength(1)
        })
        // Recovery requires two native idle observations. Use the whole-test
        // deadline for authoritative Worker state, then ordinary UI assertions.
        await waitForNativeInputQueueIdle(worker.server, agentId, currentTestDeadline)
        await waitForAgentIdle(page)
        const snapshot = await readNativeMessageSnapshot(context, agentId)
        const recovery = snapshot.messages.flatMap((message) => {
          const body = nativeMessageBody(message)
          return isObject(body) && body.type === ZCODE_METHOD.StateUpdated
            ? [
                { message, body },
              ]
            : []
        })
        expect(recovery).toHaveLength(1)
        const row = recovery[0]!
        expect(row.message.completion).toBe(failed ? MessageCompletion.ERROR : MessageCompletion.COMPLETE)
        expect(row.body).toMatchObject({
          payload: { scope: 'session', sessionId: snapshot.agentSessionId },
        })
        const evidence = readEvidence(evidencePath)
        const omitted = evidence.filter(record => record.direction === 'native' && record.forwarded === null)
        expect(omitted).toHaveLength(1)
        const boundary = evidence.filter(record => record.direction === 'native' && !record.fragment).map(record => decodeFrame(record.raw)).find(frame =>
          isObject(frame) && frame.method === ZCODE_METHOD.StateUpdated && isObject(frame.params)
          && frame.params.sessionId === snapshot.agentSessionId && (frame.params.reason === 'prompt_completed' || frame.params.reason === 'prompt_failed'))
        if (!isObject(boundary))
          throw new Error('The relay recorded no native prompt boundary.')
        expect(row.body.payload).toEqual(boundary.params)
        const ends = snapshot.messages.map(nativeMessageBody).filter(body => isObject(body) && (body.type === ZCODE_EVENT.TurnCompleted || body.type === ZCODE_EVENT.TurnFailed))
        expect(ends).toHaveLength(0)
        const dividers = visibleOnly(page.getByTestId('result-divider'))
        await expect(dividers).toHaveCount(1)
        await expect(dividers).toContainText(failed ? 'Turn failed' : 'Turn ended')
        if (!failed)
          await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
        await expectNoUnrenderedRow(page)
        await page.reload()
        await expect(dividers).toHaveCount(1)
        await expect(dividers).toContainText(failed ? 'Turn failed' : 'Turn ended')
        if (!failed)
          await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
        await expectNoUnrenderedRow(page)
        await sendNativeAnswer(context, 'Reply after recovery.', `Next answer ${marker}`)
        await expect(dividers).toHaveCount(2)
        await expectNoUnrenderedRow(page)
        const final = await readNativeMessageSnapshot(context, agentId)
        expect(final.messages.map(nativeMessageBody).filter(body => isObject(body) && (body.type === ZCODE_EVENT.TurnCompleted || body.type === ZCODE_EVENT.TurnFailed))).toHaveLength(1)
      }
      finally {
        await testInfo.attach('zcode-native-recovery-relay', { path: evidencePath, contentType: 'application/x-ndjson' })
      }
    })
  })
}
