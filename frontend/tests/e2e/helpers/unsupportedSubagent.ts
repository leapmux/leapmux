import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { RunningNativeChild } from './runningChildProof'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { AgentInputKind, EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, InterruptAgentRequestSchema, InterruptAgentResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { withCleanup } from './cleanup'
import { readNativeInputQueue } from './nativeInputQueueIdle'
import { nativeAgentsByIds } from './nativeScenario'
import { uniqueMarker } from './shellArguments'
import { openChildTabFromRow } from './subagentRegistry'
import { composerEditor, interruptButton } from './ui'

/** The reason that the composer of a read-only subagent tab states. */
export const READ_ONLY_SUBAGENT_REASON = 'This subagent doesn\'t accept messages.'

/**
 * Require the composer of the selected tab to state why the subagent accepts no message, exactly once.
 *
 * The placeholder of the editor states the reason. An earlier note above the box stated the same sentence again, so
 * a read-only subagent tab showed it twice.
 *
 * The count reads visible text only. `Tooltip` keeps an offscreen `srOnly` description in `aria-describedby` while the
 * control is disabled, which is the one route that a screen-reader user has to the reason.
 */
export async function expectReadOnlySubagentReason(page: Page): Promise<void> {
  await expect(page.locator(`[data-placeholder="${READ_ONLY_SUBAGENT_REASON}"]:visible`)).toBeVisible()
  await expect(page.getByText(READ_ONLY_SUBAGENT_REASON, { exact: true }).filter({ visible: true })).toHaveCount(0)
}

/**
 * Require that no model request of the scenario carries `text`, also a request that no rule or step answered.
 * The failure lists the rule or step of each request that carries it.
 */
export async function expectNoModelRequestCarries(modelScript: Pick<ModelScript, 'status'>, text: string): Promise<void> {
  if (text.trim() === '')
    throw new Error('A request text check needs a text that is not empty, because every request holds an empty text.')
  const status = await modelScript.status()
  const answerOf = (request: MockModelRequestRecord) => request.rule ?? (request.stepIndex === undefined ? 'the fallback' : `step ${request.stepIndex}`)
  const carriers = [
    ...status.requests.filter(request => JSON.stringify(request.body).includes(text)).map(answerOf),
    ...status.unexpectedRequests.filter(request => JSON.stringify(request.body).includes(text)).map(request => `an unexpected request: ${request.reason}`),
  ]
  expect(carriers, `no model request carries ${JSON.stringify(text)}`).toEqual([])
}

/** Check a native child route while its original task still runs. */
export async function expectUnsupportedSubagent(
  context: ManagedNativeScenarioContext,
  options: { operation: 'send' | 'interrupt', openChild: () => Promise<RunningNativeChild> },
): Promise<void> {
  const child = await options.openChild()
  // Each call refuses its own text, so a check can never pass on the text of another call.
  const refusedMessage = uniqueMarker('REFUSEDCHILDMESSAGE')
  await withCleanup(async () => {
    expect(child.childId).not.toBe('')
    expect(child.parentId).not.toBe('')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    const { hubUrl, adminToken, workerId } = context.leapmuxServer
    const agents = await nativeAgentsByIds(context, [child.childId, child.parentId])
    const info = agents.find(agent => agent.id === child.childId)
    const parent = agents.find(agent => agent.id === child.parentId)
    expect(info).toBeDefined()
    expect(parent).toBeDefined()
    if (!info || !parent)
      throw new Error('The native child or parent has no Worker record.')
    expect(info.parentAgentId).toBe(child.parentId)
    expect(info.rootAgentId).toBe(parent.rootAgentId)
    await openChildTabFromRow(context.page, child.row)
    const channel = await getTestChannel(hubUrl, adminToken)
    if (options.operation === 'interrupt') {
      expect(info.acceptsInterrupt).toBe(false)
      expect(parent.acceptsInterrupt).toBe(true)
      await expect(interruptButton(context.page)).toHaveCount(0)
      await expect(channel.callWorker(workerId, 'InterruptAgent', InterruptAgentRequestSchema, InterruptAgentResponseSchema, {
        agentId: child.childId,
      })).rejects.toMatchObject({ source: 'rpc', code: Code.FailedPrecondition, message: 'this subagent cannot be interrupted' })
    }
    else {
      expect(info.acceptsMessages).toBe(false)
      expect(parent.acceptsMessages).toBe(true)
      await expect(composerEditor(context.page)).toHaveAttribute('contenteditable', 'false')
      await expectReadOnlySubagentReason(context.page)
      const inputId = crypto.randomUUID()
      const before = await readNativeInputQueue(context.leapmuxServer, child.childId)
      await expect(channel.callWorker(workerId, 'EnqueueAgentInput', EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, {
        agentId: child.childId,
        inputId,
        text: refusedMessage,
        kind: AgentInputKind.USER_MESSAGE,
      })).rejects.toMatchObject({ source: 'rpc', code: Code.InvalidArgument, message: 'invalid queued agent input: this agent does not accept that input' })
      expect(await readNativeInputQueue(context.leapmuxServer, child.childId)).toEqual(before)
      await expectNoModelRequestCarries(context.modelScript, refusedMessage)
    }
    await expect(child.row).toHaveAttribute('data-status', 'running')
  }, () => child.finish())
  // The completed child and its parent send more model requests. None of them may carry the refused message either.
  if (options.operation === 'send')
    await expectNoModelRequestCarries(context.modelScript, refusedMessage)
}
