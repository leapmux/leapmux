import type { AgentInputProcessRecovery } from '~/generated/proto/leapmux/v1/agent_pb'
import type { InputRecoveryActions, InputRecoveryDiscovery, InputRecoveryResult, InputRecoveryTarget, InputRecoveryWorkerFailure } from '~/models/agentInputRecovery'
import type { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import type { TabView } from '~/stores/tabView'
import { clone } from '@bufbuild/protobuf'
import { workerAgentInput } from '~/api/workerRpc'
import { AgentInputProcessRecoverySchema, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { formatErrorMessage } from '~/lib/errors'
import { inputRecoveryTargetKey } from '~/models/agentInputRecovery'
import { isAgentTab } from '~/stores/tab.types'

const REQUEST_CONCURRENCY = 4

function copyRecovery(recovery: AgentInputProcessRecovery): AgentInputProcessRecovery {
  if (recovery.outputGeneration <= 0n || !recovery.agentSessionId || !recovery.processInstanceId || recovery.agentProvider === AgentProvider.UNSPECIFIED)
    throw new Error('The recovery target is incomplete. Reopen Recover login and try again.')
  return clone(AgentInputProcessRecoverySchema, recovery)
}

function copyTarget(target: InputRecoveryTarget): InputRecoveryTarget {
  if (!target.workerId || !target.agentId)
    throw new Error('Recovery requires an agent and its worker.')
  return {
    ...target,
    recovery: copyRecovery(target.recovery),
  }
}

async function mapRequests<T, R>(items: readonly T[], run: (item: T) => Promise<R>): Promise<R[]> {
  const results: R[] = []
  const entries = items.entries()
  await Promise.all(Array.from({
    length: Math.min(REQUEST_CONCURRENCY, items.length),
  }, async () => {
    // Iterator access precedes the await, so each request owns one result slot.
    for (const [index, item] of entries)
      results[index] = await run(item)
  }))
  return results
}

type WorkerDiscovery = {
  kind: 'targets'
  targets: readonly InputRecoveryTarget[]
} | {
  kind: 'failure'
  failure: InputRecoveryWorkerFailure
}

export function createAgentInputRecoveryOperations(deps: {
  view: Pick<TabView, 'all' | 'getAgentTab'>
  store: Pick<ReturnType<typeof createAgentInputQueueStore>, 'apply'>
  workerName: (workerId: string) => string
}): InputRecoveryActions {
  const captureTarget = (agentId: string, recovery: AgentInputProcessRecovery): InputRecoveryTarget => {
    const tab = deps.view.getAgentTab(agentId)
    const workerId = tab?.workerId
    if (!tab || !workerId)
      throw new Error('The worker for this tab is unavailable. Reopen the tab and try again.')
    return {
      workerId,
      workerName: deps.workerName(workerId),
      agentId,
      title: tab.title ?? '',
      recovery: copyRecovery(recovery),
    }
  }

  const discoverTargets = async (current: InputRecoveryTarget): Promise<InputRecoveryDiscovery> => {
    const captured = copyTarget(current)
    const currentKey = inputRecoveryTargetKey(captured)
    // Capture placement once. Tabs created after discovery are not approved by
    // an already open dialog, and tabs on other workers retain their channels.
    const knownTabs = deps.view.all().filter(isAgentTab).flatMap(tab => tab.workerId
      ? [
          { workerId: tab.workerId, agentId: tab.id },
        ]
      : [])
    const knownKeys = new Set(knownTabs.map(inputRecoveryTargetKey))
    knownKeys.add(currentKey)
    const workerIds = [
      ...new Set([
        captured.workerId,
        ...knownTabs.map(tab => tab.workerId),
      ]),
    ]
    const readings = await mapRequests<string, WorkerDiscovery>(workerIds, async (workerId) => {
      const workerName = deps.workerName(workerId)
      try {
        const response = await workerAgentInput.listAgentInputRecoveries(workerId, { agentProvider: captured.recovery.agentProvider })
        const targets: InputRecoveryTarget[] = []
        const seen = new Set<string>()
        for (const record of response.targets) {
          if (!record.agentId)
            throw new Error('The worker returned an invalid recovery target. Retry the checks.')
          const key = inputRecoveryTargetKey({ workerId, agentId: record.agentId })
          if (!knownKeys.has(key))
            continue
          if (!record.recovery || record.recovery.agentProvider !== captured.recovery.agentProvider || seen.has(key))
            throw new Error('The worker returned an invalid recovery target. Retry the checks.')
          seen.add(key)
          const target: InputRecoveryTarget = {
            workerId,
            workerName,
            agentId: record.agentId,
            title: record.title,
            recovery: copyRecovery(record.recovery),
          }
          if (key !== currentKey)
            targets.push(target)
        }
        return { kind: 'targets', targets }
      }
      catch (error) {
        return {
          kind: 'failure',
          failure: {
            workerId,
            workerName,
            message: formatErrorMessage(error, 'The worker could not be checked.'),
          },
        }
      }
    })
    const related: InputRecoveryTarget[] = []
    const failures: InputRecoveryWorkerFailure[] = []
    for (const reading of readings) {
      if (reading.kind === 'failure')
        failures.push(reading.failure)
      else
        related.push(...reading.targets)
    }
    related.sort((left, right) => left.workerName.localeCompare(right.workerName) || left.title.localeCompare(right.title) || left.agentId.localeCompare(right.agentId))
    return {
      current: captured,
      targets: [captured, ...related],
      failures,
    }
  }

  const recoverTargets = async (targets: readonly InputRecoveryTarget[]): Promise<readonly InputRecoveryResult[]> => {
    const captured = Array.from(targets, copyTarget)
    const keys = captured.map(inputRecoveryTargetKey)
    if (new Set(keys).size !== keys.length)
      throw new Error('Each tab can occur only once in a recovery approval.')
    return mapRequests<InputRecoveryTarget, InputRecoveryResult>(captured, async (target) => {
      try {
        const response = await workerAgentInput.recoverAgentInputProcess(target.workerId, { agentId: target.agentId, recovery: target.recovery })
        if (!response.snapshot)
          throw new Error('Recovery could not be confirmed. Check this tab before trying again.')
        deps.store.apply(target.agentId, response.snapshot)
        if (response.snapshot.processRecovery)
          throw new Error('The worker still reports a login issue. Sign in on that worker, then reopen Recover login.')
        return { target, status: 'recovered' }
      }
      catch (error) {
        return {
          target,
          status: 'unconfirmed',
          message: formatErrorMessage(error, 'Recovery could not be confirmed.'),
        }
      }
    })
  }
  return { captureTarget, discoverTargets, recoverTargets }
}
