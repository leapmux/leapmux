import { isLettaToolProgress } from '../../../src/components/chat/providers/letta/toolOutput'
import { isObject } from '../../../src/lib/jsonPick'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expect, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('exposes no token or byte counter throughout the completed native stream', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: false, counter: 'tokens', approveTool: false })
})

lettaTest('reports no byte count throughout an actual native shell output stream', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: false, counter: 'bytes' })
})

lettaTest('replaces live native shell windows and retains one actual final result', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  await exerciseGenerationProgress(context, {
    supported: false,
    counter: 'bytes',
    afterOutputBoundary: async (boundary) => {
      const current = boundary.phase === 'first' ? boundary.firstMarker : boundary.secondMarker
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      await testInfo.attach(`letta-live-native-before-assertion-${boundary.phase}`, {
        body: JSON.stringify({
          agentId: snapshot.agentId,
          sessionId: snapshot.agentSessionId,
          messages: snapshot.messages.map(message => ({ id: message.id, seq: message.seq.toString(), spanId: message.spanId, spanType: message.spanType, completion: message.completion, agentSessionId: message.agentSessionId, frame: nativeMessageBody(message) })),
        }),
        contentType: 'application/json',
      })
      const live = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-progress-output"][data-tool-row-role="request"]:visible')
      await expect(live).toHaveCount(1)
      await expect(live).toBeVisible()
      // The shared publisher sends only the last 2 KiB. Native markers remain in the retained records.
      const tail = boundary.phase === 'first' ? boundary.firstLiveTail : boundary.secondLiveTail
      await expect(live).toContainText(tail)
      if (boundary.phase === 'second')
        await expect(live).not.toContainText(boundary.firstLiveTail)
      const result = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-progress-output"][data-tool-row-role="result"]:visible')
      await expect(result).toHaveCount(0)
      const currentSnapshot = await readNativeMessageSnapshot(context, agent.id)
      const progress = currentSnapshot.messages.map(nativeMessageBody).filter(isObject).filter(isLettaToolProgress)
      expect(progress.length).toBeGreaterThan(0)
      expect(progress.at(-1)?.tool_return).toContain(current)
      await testInfo.attach(`letta-live-native-window-${boundary.phase}`, { body: JSON.stringify({ sessionId: currentSnapshot.agentSessionId, progress }), contentType: 'application/json' })
    },
  })
  const result = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-progress-output"][data-tool-row-role="result"]:visible')
  await expect(result).toHaveCount(1)
  await expect(result).toHaveAttribute('data-tool-status', 'completed')
})
