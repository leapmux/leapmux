import type { CreateTunnelResult, ListTunnelsResult, TunnelConfig, TunnelInfo } from '~/api/platformBridge'
import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/context/AuthContext'
import { createSignal } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { resolveTunnelTrust } from '~/api/workerRpc'
import { formatErrorMessage } from '~/lib/errors'
import { createIdentityCache } from '~/lib/identityCache'
import { createLogger } from '~/lib/logger'
import { shallowEqual } from '~/lib/shallowEqual'

const log = createLogger('tunnels')

interface TunnelSnapshot extends ListTunnelsResult {
  scope: WorkerTrustScope
}

interface RefreshFlight {
  scope: WorkerTrustScope
  revision: number
  done: Promise<void>
}

type RefreshMode = 'coalesce' | 'invalidate'

export interface TunnelStore {
  tunnels: () => TunnelInfo[]
  tunnelsForWorker: (workerId: string) => TunnelInfo[]
  refresh: (scope: WorkerTrustScope, mode?: RefreshMode) => Promise<void>
  add: (config: TunnelConfig, scope: WorkerTrustScope, approval?: TunnelKeyApproval) => Promise<CreateTunnelResult>
  remove: (tunnelId: string, scope: WorkerTrustScope) => Promise<void>
  removeAllForWorker: (workerId: string, scope: WorkerTrustScope) => Promise<void>
}

export function createTunnelStore(getScope: () => WorkerTrustScope): TunnelStore {
  const [snapshot, setSnapshot] = createSignal<TunnelSnapshot>()
  let inFlight: RefreshFlight | undefined
  const rowIdentity = createIdentityCache<TunnelInfo>({
    keyOf: tunnel => tunnel.id,
    equals: ({ keyMismatch: previousMismatch, ...previous }, { keyMismatch, ...current }) =>
      shallowEqual(previous, current)
      && previousMismatch?.expectedFingerprint === keyMismatch?.expectedFingerprint
      && previousMismatch?.presentedFingerprint === keyMismatch?.presentedFingerprint,
  })

  function isCurrent(scope: WorkerTrustScope): boolean {
    return scope === getScope() && !scope.signal.aborted && Boolean(scope.userId)
  }

  function requireCurrent(scope: WorkerTrustScope): void {
    if (!isCurrent(scope))
      throw new Error('The account changed. Open the tunnel action again.')
  }

  function tunnels(): TunnelInfo[] {
    const current = snapshot()
    const scope = getScope()
    return current?.scope === scope && !scope.signal.aborted
      ? current.tunnels
      : []
  }

  async function refresh(scope: WorkerTrustScope, mode: RefreshMode = 'coalesce'): Promise<void> {
    requireCurrent(scope)
    if (inFlight?.scope === scope) {
      if (mode === 'invalidate')
        inFlight.revision++
      return inFlight.done
    }

    const next: RefreshFlight = {
      scope,
      revision: 0,
      done: Promise.resolve().then(async () => {
        let result: ListTunnelsResult
        try {
          // A mutation or native hint received mid-list invalidates that
          // reply. All invalidations already received share the next read.
          for (;;) {
            requireCurrent(scope)
            const revision = next.revision
            try {
              result = await platformBridge.listTunnels(scope)
              requireCurrent(scope)
              if (next.revision === revision)
                break
            }
            catch (err) {
              if (!isCurrent(scope) || next.revision === revision)
                throw err
            }
          }
        }
        finally {
          // Release ownership before publishing. A reactive observer can
          // invalidate the list synchronously when it sees the snapshot.
          if (inFlight === next)
            inFlight = undefined
        }

        const previous = snapshot()
        if (previous?.scope !== scope || previous.channelEpoch !== result.channelEpoch)
          rowIdentity.clear()
        setSnapshot({
          ...result,
          scope,
          tunnels: rowIdentity.stabilize(result.tunnels),
        })
      }),
    }
    inFlight = next
    return next.done
  }

  async function reconcileDelete(scope: WorkerTrustScope, remove: () => Promise<void>): Promise<void> {
    let deletion: PromiseSettledResult<void>
    try {
      await remove()
      deletion = { status: 'fulfilled', value: undefined }
    }
    catch (reason) {
      deletion = { status: 'rejected', reason }
    }
    requireCurrent(scope)
    try {
      await refresh(scope, 'invalidate')
    }
    catch (err) {
      if (deletion.status === 'rejected')
        throw deletion.reason
      throw new Error('The desktop tunnel list could not be refreshed. Try again.', { cause: err })
    }
    if (deletion.status === 'rejected')
      throw deletion.reason
  }

  return {
    tunnels,
    tunnelsForWorker: workerId => tunnels().filter(t => t.workerId === workerId),
    refresh,
    add: async (config, scope, approval) => {
      requireCurrent(scope)
      const result = await platformBridge.createTunnel(config, scope, () =>
        resolveTunnelTrust(config.workerId, scope, approval))
      requireCurrent(scope)
      try {
        await refresh(scope, 'invalidate')
      }
      catch (err) {
        // Native creation already returned an outcome. A list failure must
        // not report that a successfully created listener failed to bind.
        if (isCurrent(scope))
          log.warn('Failed to list the tunnels after creation', err)
      }
      requireCurrent(scope)
      return result
    },
    remove: async (tunnelId, scope) => {
      requireCurrent(scope)
      await reconcileDelete(scope, () => platformBridge.deleteTunnel(tunnelId, scope))
    },
    removeAllForWorker: async (workerId, scope) => {
      requireCurrent(scope)
      const tunnelIds = tunnels().filter(t => t.workerId === workerId).map(t => t.id)
      if (tunnelIds.length === 0)
        return

      await reconcileDelete(scope, async () => {
        const results = await platformBridge.deleteTunnels(tunnelIds, scope)
        const failures = results.filter(result => result.status === 'rejected')
        if (failures.length === 0)
          return
        const cause = new AggregateError(failures.map(result => result.reason), 'Desktop tunnel deletions failed.')
        if (failures.length === results.length)
          throw new Error(formatErrorMessage(failures[0]!.reason, 'Could not delete the tunnels. Try again.'), { cause })
        const message = failures.length === 1
          ? 'One tunnel could not be deleted. Try again to delete it.'
          : `${failures.length} tunnels could not be deleted. Try again to delete them.`
        throw new Error(message, { cause })
      })
    },
  }
}
