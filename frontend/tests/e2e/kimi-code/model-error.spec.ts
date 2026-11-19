import { expect } from '@playwright/test'
import { kimiConversationRow } from '../../../src/components/chat/providers/kimi/conversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseModelError, nativeErrorMarker } from '../helpers/nativeModelError'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { messageBubbles, waitForAgentIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest('shows the native model failure and accepts the next valid prompt', async ({ native }, testInfo) => {
  try {
    const failure = {
      status: 400,
      code: 'invalid_request_error',
      message: nativeErrorMarker(),
    }
    // The native turn retains its error for restored history. A separate
    // non-replayed notice carries the live diagnostic detail.
    const notifications = messageBubbles(native.page).and(native.page.locator('[data-category="notification"]'))
    await exerciseModelError(native, {
      queueAfterFailure: 'running',
      error: failure,
      errorContents: notifications.locator('[data-testid="message-content"]'),
    })
    const current = await currentNativeAgent(native)
    for (const reload of [false, true]) {
      if (reload) {
        await native.page.reload()
        await waitForAgentIdle(native.page)
      }
      const snapshot = await readNativeMessageSnapshot(native, current.id)
      const failed = snapshot.messages.flatMap((message) => {
        const row = kimiConversationRow(nativeMessageBody(message))
        return row?.kind === 'turn' && row.turn.outcome === 'failed'
          ? [row]
          : []
      })
      expect(failed).toHaveLength(1)
      expect(failed[0]).toMatchObject({
        sessionId: snapshot.agentSessionId,
        agentId: 'main',
        turn: {
          kind: 'agent',
          status: 'ended',
          end_reason: 'error',
          error: {
            code: 'provider.api_error',
            message: failure.message,
            status_code: 400,
            retryable: false,
          },
        },
      })
      await expect(native.page.locator('[data-testid="result-divider"]:visible').filter({ hasText: failure.message })).toHaveCount(1)
      await expect(notifications.filter({ hasText: failure.message })).toHaveCount(1)
    }
  }
  catch (error) {
    await Promise.allSettled([
      (async () => {
        const current = await currentNativeAgent(native)
        const snapshot = await readNativeMessageSnapshot(native, current.id)
        await testInfo.attach('kimi-model-error-native-rows', {
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
