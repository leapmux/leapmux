import { expect } from '@playwright/test'
import { AgentInputState, AgentProvider, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { getTestChannel } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { uniqueMarker } from '../helpers/shellArguments'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

cursorTest('queues a normal prompt until the native turn ends without a steering route', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const agent = await currentNativeAgent(context)
  const suffix = uniqueMarker()
  const gate = `cursor-no-steer-${suffix}`
  const queuedText = `CURSORQUEUED${suffix}: deliver after the first turn ends.`
  const start = (await modelScript.status()).stepCount
  await withCleanup(async () => {
    await modelScript.queue({ text: 'The actual first native turn ended.', gate }, { text: 'The actual queued native turn ended.' })
    await sendMessage(page, modelScript.prompt('Run the controlled first native turn.'))
    await modelScript.waitForGate(gate)
    await sendMessage(page, modelScript.prompt(queuedText))
    const queued = queuedInputRow(page, queuedText)
    await expect(queued).toBeVisible()
    await expect(steerButton(queued)).toHaveCount(0)
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const snapshot = await channel.callWorker(leapmuxServer.workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId: agent.id })
    const item = snapshot.snapshot?.items.find(input => input.text.includes(queuedText))
    expect(item?.state).toBe(AgentInputState.QUEUED)
    expect((await modelScript.status()).requests.some(record => record.stepIndex === start + 1)).toBe(false)
    await modelScript.releaseGate(gate)
    const status = await modelScript.waitForSteps(start + 2)
    const next = status.requests.find(record => record.stepIndex === start + 1)
    expect(JSON.stringify(next?.body)).toContain(queuedText)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The actual queued native turn ended.' }).first()).toBeVisible()
  }, () => modelScript.releaseGateIfHeld(gate).then(() => {}))
})
