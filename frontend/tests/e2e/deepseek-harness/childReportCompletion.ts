import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { deepseekHarnessEventData } from '../../../src/components/chat/providers/deepseekharness/protocol'
import { DEEPSEEK_HARNESS_EVENT } from '../../../src/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { nativeAgentById } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { deepseekHarnessChildReportRule } from './childReports'

export async function finishDeepseekHarnessChild(finishChild: () => Promise<void>, waitForReport: () => Promise<void>): Promise<void> {
  await finishChild()
  await waitForReport()
}

/** Match the parent completion to the native turn that made this exact report request. */
export function deepseekHarnessCompletedReport(request: MockModelRequestRecord, parentSessionId: string, frames: readonly unknown[]): boolean {
  if (!request.response)
    return false
  if (request.response.status !== 200)
    throw new Error(`The native parent report response failed with status ${request.response.status}.`)
  if (request.protocol !== 'anthropic-messages' || !parentSessionId)
    throw new Error('The native parent report requires its exact Session and model protocol.')
  const body = isObject(request.body) ? request.body : undefined
  const log = pickObject(body, 'dsh_session_log')
  if (pickObject(log, 'session')?.id !== parentSessionId || !Array.isArray(log?.events))
    throw new Error('The native parent report request does not belong to the stored parent Session.')
  const start = log.events.findLast((event: unknown) => isObject(event) && event.type === DEEPSEEK_HARNESS_EVENT.TurnStart)
  const turn = isObject(start) ? pickObject(start, 'data')?.turn : undefined
  if (typeof turn !== 'number' || !Number.isSafeInteger(turn) || turn < 1)
    throw new Error('The native parent report has no exact native turn identity.')
  const endings = frames.map(frame => deepseekHarnessEventData(frame, DEEPSEEK_HARNESS_EVENT.TurnEnd)).filter(data => data?.turn === turn)
  if (endings.length > 1)
    throw new Error('The native parent report has duplicate stored turn completions.')
  if (endings.length === 0)
    return false
  const reason = pickObject(endings[0], 'reason')?.kind
  if (reason !== 'completed')
    throw new Error(`The native parent report did not complete: ${String(reason)}.`)
  return true
}

/** Wait for one exact report response and its completed Worker turn before later input. */
export async function waitForDeepseekHarnessChildReport(context: ManagedNativeScenarioContext, childAgentId: string, parentAgentId: string, reportNumber = 1): Promise<void> {
  if (!childAgentId || !parentAgentId || childAgentId === parentAgentId || !Number.isSafeInteger(reportNumber) || reportNumber < 1)
    throw new Error('The native child report requires distinct stored owners and a positive report count.')
  const [child, parent] = await Promise.all([nativeAgentById(context, childAgentId), nativeAgentById(context, parentAgentId)])
  if (!child?.agentSessionId || !parent?.agentSessionId || child.parentAgentId !== parentAgentId)
    throw new Error('The native child report has no exact stored parent and child Session owners.')
  const rule = deepseekHarnessChildReportRule(child.agentSessionId)
  const server = context.leapmuxServer
  await retryUntilPass(async () => {
    const status = await context.modelScript.status()
    const count = status.ruleMatches[rule.name] ?? 0
    if (count > reportNumber)
      throw new Error('The native child produced more parent reports than the scenario expects.')
    expect(count, 'the native child sent its report to the parent').toBe(reportNumber)
    const reports = status.requests.filter(request => request.rule === rule.name)
    const request = reports[reportNumber - 1]
    if (reports.length !== reportNumber || !request)
      throw new Error('The native child report count does not match its exact recorded requests.')
    expect(request.response, 'the mock answered the report request').toBeDefined()
    const snapshot = await readNativeMessageSnapshot(context, parentAgentId)
    if (snapshot.agentSessionId !== parent.agentSessionId)
      throw new Error('The native parent Session changed before its child report completed.')
    expect(deepseekHarnessCompletedReport(request, parent.agentSessionId, snapshot.messages.map(nativeMessageBody)), 'the Worker stores the completed parent turn of the report')
      .toBe(true)
    expect((await readNativeInputQueue(server, parentAgentId)).activeTurn, 'the native parent ended its report turn').toBe(false)
  })
}
