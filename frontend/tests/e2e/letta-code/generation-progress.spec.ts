import { expect } from '@playwright/test'
import { isLettaToolProgress } from '../../../src/components/chat/providers/letta/toolOutput'
import { isObject } from '../../../src/lib/jsonPick'
import { exerciseOutputByteProgress, exerciseTokenProgress, PROGRESS_OUTPUT_CALL_ID } from '../helpers/generationProgress'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { toolCallRow } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'

lettaTest('exposes no token or byte counter throughout the completed native stream', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: false })
})

lettaTest('reports no byte count throughout an actual native shell output stream', async ({ native }) => {
  await exerciseOutputByteProgress(native, { supported: false })
})

lettaTest('replaces live native shell windows and retains one actual final result', async ({ native }, testInfo) => {
  const agent = await currentNativeAgent(native)
  await exerciseOutputByteProgress(native, {
    supported: false,
    afterOutputBoundary: async (boundary) => {
      const current = boundary.phase === 'first' ? boundary.firstMarker : boundary.secondMarker
      const snapshot = await readNativeMessageSnapshot(native, agent.id)
      await testInfo.attach(`letta-live-native-before-assertion-${boundary.phase}`, {
        body: JSON.stringify({
          agentId: snapshot.agentId,
          sessionId: snapshot.agentSessionId,
          messages: snapshot.messages.map(message => ({ id: message.id, seq: message.seq.toString(), spanId: message.spanId, spanType: message.spanType, completion: message.completion, agentSessionId: message.agentSessionId, frame: nativeMessageBody(message) })),
        }),
        contentType: 'application/json',
      })
      const live = toolCallRow(native.page, PROGRESS_OUTPUT_CALL_ID, 'request')
      await expect(live).toHaveCount(1)
      await expect(live).toBeVisible()
      // The shared publisher sends only the last 2 KiB. Native markers remain in the retained records.
      const tail = boundary.phase === 'first' ? boundary.firstLiveTail : boundary.secondLiveTail
      await expect(live).toContainText(tail)
      if (boundary.phase === 'second')
        await expect(live).not.toContainText(boundary.firstLiveTail)
      await expect(toolCallRow(native.page, PROGRESS_OUTPUT_CALL_ID)).toHaveCount(0)
      const currentSnapshot = await readNativeMessageSnapshot(native, agent.id)
      const progress = currentSnapshot.messages.map(nativeMessageBody).filter(isObject).filter(isLettaToolProgress)
      expect(progress.length).toBeGreaterThan(0)
      expect(progress.at(-1)?.tool_return).toContain(current)
      await testInfo.attach(`letta-live-native-window-${boundary.phase}`, { body: JSON.stringify({ sessionId: currentSnapshot.agentSessionId, progress }), contentType: 'application/json' })
    },
  })
  const result = toolCallRow(native.page, PROGRESS_OUTPUT_CALL_ID)
  await expect(result).toHaveCount(1)
  await expect(result).toHaveAttribute('data-tool-status', 'completed')
})
