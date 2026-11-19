import { expect } from '@playwright/test'
import { INTERRUPTION_MARKER } from '../../../src/components/chat/assembledMessage'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest } from '../dirac-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { writeChildMarkerFile } from '../helpers/liveChildTranscript'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { diracRespondToolCall, readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { backgroundTaskRows, expandBackgroundTasksSection, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, interruptButton, messageContents, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'
import { diracChildRequestMatcher } from './childRequest'
import { runningChild } from './scenarios'

/** The call ID of the native Read of the child. */
const READ_CALL_ID = 'native-live-read'

diracTest('keeps a native child partial answer and its interruption marker after the parent stops', async ({ native }, testInfo) => {
  const { page, modelScript } = native
  const parent = await currentNativeAgent(native)
  const task = modelScript.prompt('DIRACPARTIALCHILDTASK stream the scripted answer.')
  const head = 'DIRACCHILDPARTIALHEAD'
  const tail = 'DIRACCHILDNEVERSTREAMED'
  const childGate = 'dirac-child-partial-chunk'
  const parentGate = 'dirac-partial-parent-final'
  await modelScript.rule({
    name: 'dirac-child-partial-stream',
    when: diracChildRequestMatcher(task),
    respond: {
      text: `${head} ${tail}`,
      stream: {
        chunkChars: head.length,
        delayMs: 0,
        gates: [
          { afterChunk: 1, name: childGate },
        ],
      },
    },
    once: true,
  })
  await modelScript.queue({
    toolCalls: [
      spawnSubagentToolCall(native.provider, 'native-partial-child', { description: 'Partial native child', prompt: task }),
    ],
  }, {
    toolCalls: [
      diracRespondToolCall('partial-parent-complete', 'complete', 'The parent completed.'),
    ],
    gate: parentGate,
  })
  modelScript.allowUnconsumed('The parent interrupt cancels the held parent response and the child stream.')
  await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Create the scripted child and wait for its answer.'))
    await modelScript.waitForGate(childGate)
    await modelScript.waitForGate(parentGate)
    await expandBackgroundTasksSection(page)
    const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'Partial native child' }).first()
    const childID = await openChildTabFromRow(page, row)
    await expect(assistantBubbles(page).filter({ hasText: head })).toHaveCount(1)
    await tabById(page, parent.id).click()
    await interruptButton(page).click()
    await waitForAgentIdle(page)
    await expect(row).toHaveAttribute('data-status', 'stopped')
    await tabById(page, childID).click()
    await retryUntilPass(async () => {
      const snapshot = await readNativeMessageSnapshot(native, childID)
      expect(snapshot.messages.filter(message => JSON.stringify(nativeMessageBody(message)).includes(head)).map(message => message.completion))
        .toEqual([MessageCompletion.INTERRUPTED])
    })
    try {
      for (const reload of [false, true]) {
        if (reload)
          await page.reload()
        await expect(assistantBubbles(page).filter({ hasText: head }).filter({ hasText: INTERRUPTION_MARKER })).toHaveCount(1)
        await expect(messageContents(page).filter({ hasText: tail })).toHaveCount(0)
      }
    }
    catch (error) {
      const snapshot = await readNativeMessageSnapshot(native, childID)
      await testInfo.attach('dirac-partial-render-state', {
        contentType: 'application/json',
        body: JSON.stringify({
          messages: snapshot.messages.map(message => ({
            id: message.id,
            completion: message.completion,
            revision: String(message.supplementalRevision),
            body: nativeMessageBody(message),
          })),
        }),
      })
      throw error
    }
  }, () => finishCleanup([
    modelScript.releaseGateIfHeld(childGate),
    modelScript.releaseGateIfHeld(parentGate),
  ]))
})

diracTest('streams the actual native child Read and preserves it after completion and reload', async ({ native }, testInfo) => {
  const parent = await currentNativeAgent(native)
  try {
    const file = writeChildMarkerFile(parent.workingDir, 'native-child-read.txt')
    const child = await runningChild(native, {
      childTool: readToolCall(native.provider, READ_CALL_ID, file.path),
    })
    const output = () => messageContents(native.page).filter({ hasText: file.marker })
    await withCleanup(async () => {
      await openChildTabFromRow(native.page, child.row)
      await expect(output()).toHaveCount(1)
      await expect(child.row).toHaveAttribute('data-status', 'running')
      await native.page.reload()
      await expect(output()).toHaveCount(1)
    }, child.finish)
    await tabById(native.page, child.childId).click()
    await expect(output()).toHaveCount(1)
    await native.page.reload()
    await waitForAgentIdle(native.page)
    await expect(output()).toHaveCount(1)
  }
  catch (error) {
    await Promise.allSettled([
      (async () => {
        const snapshot = await readNativeMessageSnapshot(native, parent.id)
        await testInfo.attach('dirac-native-root-rows', {
          contentType: 'application/json',
          body: JSON.stringify({
            nativeSessionId: snapshot.agentSessionId,
            messages: snapshot.messages.map(message => ({
              id: message.id,
              seq: String(message.seq),
              body: nativeMessageBody(message),
            })),
          }),
        })
      })(),
    ])
    throw error
  }
})
