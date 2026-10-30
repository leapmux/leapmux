import { expect } from '@playwright/test'
import { KIMI_FRAME, KIMI_INTERACTION_KIND, KIMI_TOOL } from '../../../src/generated/contracts/kimi-protocol'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseRememberedAllow } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { savedControlAnswer } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest('preserves native interaction identity and a saved session approval', async ({ native }, testInfo) => {
  const agent = await currentNativeAgent(native)
  const watch = await watchNativeControls(native.leapmuxServer, agent.id)
  const command = 'echo kimi-interaction-$((40 + 2))'
  try {
    await exerciseRememberedAllow(native, {
      scope: 'Session',
      firstCall: bashToolCall(native.provider, 'interaction-first', command),
      secondCall: bashToolCall(native.provider, 'interaction-second', command),
      firstProof: request => expect(nativeToolResult(request, 'interaction-first')).toContain('kimi-interaction-42'),
      secondProof: request => expect(nativeToolResult(request, 'interaction-second')).toContain('kimi-interaction-42'),
    })
    const observed = onlyObservedNativeControl(watch.controls())
    expect(observed.payload).toMatchObject({
      type: KIMI_FRAME.Interaction,
      kind: KIMI_INTERACTION_KIND.Approval,
      status: 'pending',
      interaction_id: observed.requestId,
      session_id: agent.agentSessionId,
      agent_id: 'main',
      tool_call_id: 'interaction-first',
      request: {
        tool_name: KIMI_TOOL.Bash,
        tool_input_display: { command },
      },
    })
    expect(observed.payload).not.toHaveProperty('seq')
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const stored = readNativeStoredControlDecision(snapshot, observed.requestId)
    expect(stored.request).toEqual(observed.payload)
    expect(stored.response).toMatchObject({
      type: 'control_response',
      response: {
        request_id: observed.requestId,
        response: { decision: 'approved', scope: 'session' },
      },
    })
    await testInfo.attach('kimi-native-decision', {
      body: JSON.stringify({ request: stored.request, response: stored.response }, null, 2),
      contentType: 'application/json',
    })
    await native.page.reload()
    await expect(savedControlAnswer(native.page)).toHaveText('Allow for this session')
  }
  finally {
    watch.cancel()
  }
})
