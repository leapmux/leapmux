import { expect } from '@playwright/test'
import { kimiConversationRow } from '../../../src/components/chat/providers/kimi/conversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('applies Kimi Code session settings', () => {
  kimiTest('steers a queued message into the active turn', async ({ native }, testInfo) => {
    await waitForSettingsHydrated(native.page)
    await applyPermissionPreset(native.page, 'bypass')
    try {
      await exerciseSteerBeforeTool(native, { resultDividers: 2 })
      const current = await currentNativeAgent(native)
      const snapshot = await readNativeMessageSnapshot(native, current.id)
      const turns = snapshot.messages.flatMap((message) => {
        const row = kimiConversationRow(nativeMessageBody(message))
        return row?.kind === 'turn'
          ? [row]
          : []
      })
      expect(turns).toHaveLength(2)
      expect(new Set(turns.map(row => row.turnId)).size).toBe(2)
      expect(turns.map(row => ({
        session: row.sessionId,
        agent: row.agentId,
        kind: row.turn.kind,
        ordinal: row.turn.ordinal,
        status: row.turn.status,
        outcome: row.turn.outcome,
      }))).toEqual([
        {
          session: snapshot.agentSessionId,
          agent: 'main',
          kind: 'agent',
          ordinal: 0,
          status: 'ended',
          outcome: 'completed',
        },
        {
          session: snapshot.agentSessionId,
          agent: 'main',
          kind: 'agent',
          ordinal: 1,
          status: 'ended',
          outcome: 'completed',
        },
      ])
      expect(turns[0]?.turn.steps).toMatchObject([
        { index: 1, outcome: 'interrupted' },
      ])
      expect(turns[1]?.turn.steps).toMatchObject([
        { index: 1, outcome: 'completed' },
      ])
    }
    catch (error) {
      const [read] = await Promise.allSettled([
        (async () => {
          const current = await currentNativeAgent(native)
          return readNativeMessageSnapshot(native, current.id)
        })(),
      ])
      await testInfo.attach('kimi-steering-native-rows', {
        contentType: 'application/json',
        body: JSON.stringify(read.status === 'fulfilled'
          ? {
              agentId: read.value.agentId,
              nativeSessionId: read.value.agentSessionId,
              messages: read.value.messages.map(message => ({
                id: message.id,
                seq: String(message.seq),
                body: nativeMessageBody(message),
              })),
            }
          : {
              readError: String(read.reason),
            }),
      })
      throw error
    }
  })
})
