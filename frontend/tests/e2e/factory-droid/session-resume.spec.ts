import type { ConsoleMessage } from '@playwright/test'
import { toJson } from '@bufbuild/protobuf'
import { AgentChatMessageSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, droidTest } from '../droid-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { messageBubbles, readAttached } from '../helpers/ui'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { DROID_TITLE_RULE } from './housekeeping'
import { DROID_AGENT, nativeContext } from './scenarios'

droidTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({
  agent: DROID_AGENT,
  nativeContext,
  skipReason: DROID_E2E_SKIP_REASON,
  modelRules: [DROID_TITLE_RULE],
})
restartTest.use({ debugLogging: true })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }, testInfo) => {
    const server = separateHubWorker
    const agent = await currentNativeAgent(native)
    const consoleRecords: Promise<unknown>[] = []
    const recordConsole = (message: ConsoleMessage) => {
      consoleRecords.push(Promise.all(message.args().map(argument => argument.jsonValue())).then(
        args => ({
          type: message.type(),
          text: message.text(),
          args,
        }),
        error => ({
          type: message.type(),
          text: message.text(),
          error: String(error),
        }),
      ))
    }
    native.page.on('console', recordConsole)
    await withCleanup(async () => {
      await exerciseWorkerRestart(native, server, restart)
    }, async () => {
      native.page.off('console', recordConsole)
      await finishCleanup([
        readNativeMessageSnapshot(native, agent.id).then(snapshot => testInfo.attach('restart-worker-messages', {
          contentType: 'application/json',
          body: JSON.stringify(snapshot.messages.map(message => ({
            message: toJson(AgentChatMessageSchema, message),
            frame: nativeMessageBody(message),
          }))),
        })),
        readAttached(messageBubbles(native.page), 'the restart transcript', rows => rows.every(row => row.isConnected)
          ? rows.map(row => ({
              seq: row.getAttribute('data-message-seq'),
              text: row.textContent,
            }))
          : null).then(rows => testInfo.attach('restart-browser-messages', {
          contentType: 'application/json',
          body: JSON.stringify(rows),
        })),
        Promise.all(consoleRecords).then(records => testInfo.attach('restart-browser-console', {
          contentType: 'application/json',
          body: JSON.stringify(records),
        })),
      ])
    })
  })
}
