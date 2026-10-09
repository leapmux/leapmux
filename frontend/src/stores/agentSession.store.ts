import type { GoalProgress } from './chatGoal'
import type { ContextUsageInfo, RateLimitInfo } from '~/models/agentSession'
import { getOwner, onCleanup } from 'solid-js'
import { createStore, reconcile } from 'solid-js/store'
import { localStorageLoad, localStorageStore, onStorageAccountChange, PREFIX_AGENT_SESSION } from '~/lib/browserStorage'
import { shallowEqual } from '~/lib/shallowEqual'

export interface AgentSessionInfo {
  totalCostUsd?: number
  contextUsage?: ContextUsageInfo
  rateLimits?: Record<string, RateLimitInfo>
  planFilePath?: string
}

export interface LiveGenerationProgress {
  revision: number
  thinkingTokens?: number
  output?: { bytes: number, minimum: boolean }
}

/** Identify the exact watch request and stored sequence that delivered metadata. */
export interface SessionMetadataDelivery {
  phase: 'live' | 'replay'
  replayId?: bigint
  seq?: bigint
}

type SessionField = keyof AgentSessionInfo
type ReceiptField = SessionField | `goalProgress:${keyof GoalProgress}`

// A new progress field must supply a clear rule.
const GOAL_PROGRESS_FIELDS = {
  tokensUsed: true,
  tokenBudget: true,
  timeUsedSeconds: true,
  iterations: true,
} satisfies Record<keyof Required<GoalProgress>, true>

interface ReplayReceipt {
  replayId: bigint
  active: boolean
  liveFields: Map<ReceiptField, bigint | null>
}

interface StorageRead {
  pending: Promise<ReadonlySet<SessionField>> | null
  touched: Set<SessionField>
}

/** Use the post-compaction total and retain the known context window. */
export function compactionContextUsage(
  contextTokens: number,
  existing: ContextUsageInfo | undefined,
): ContextUsageInfo {
  return {
    inputTokens: 0,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
    contextTokens,
    ...(existing?.contextWindow !== undefined ? { contextWindow: existing.contextWindow } : {}),
  }
}

async function loadFromStorage(agentId: string): Promise<AgentSessionInfo> {
  return (await localStorageLoad<AgentSessionInfo>(`${PREFIX_AGENT_SESSION}${agentId}`)) ?? {}
}

function saveToStorage(agentId: string, info: AgentSessionInfo) {
  localStorageStore(`${PREFIX_AGENT_SESSION}${agentId}`, info)
}

interface AgentSessionStoreState {
  infoByAgent: Record<string, AgentSessionInfo>
  progressByAgent: Record<string, LiveGenerationProgress>
}

export interface AgentSessionUpdateOptions {
  delivery?: SessionMetadataDelivery
  rateLimits?: {
    mode: 'merge' | 'replace'
    deleteKeys?: readonly string[]
  }
}

function rateLimitMapsEqual(
  left: Record<string, RateLimitInfo> | undefined,
  right: Record<string, RateLimitInfo>,
): boolean {
  if (left === undefined)
    return Object.keys(right).length === 0
  if (Object.keys(left).length !== Object.keys(right).length)
    return false
  return Object.entries(right).every(([key, value]) => shallowEqual(left[key], value))
}

export function createAgentSessionStore() {
  const [state, setState] = createStore<AgentSessionStoreState>({
    infoByAgent: {},
    progressByAgent: {},
  })
  const reads = new Map<string, StorageRead>()
  const replays = new Map<string, ReplayReceipt>()
  let ownerClosed = false
  let pendingOperations = 0

  const unsubscribe = onStorageAccountChange(() => {
    reads.clear()
    replays.clear()
    setState(reconcile({ infoByAgent: {}, progressByAgent: {} }))
  })
  const finishOperation = () => {
    pendingOperations--
    if (ownerClosed && pendingOperations === 0)
      unsubscribe()
  }
  if (getOwner()) {
    onCleanup(() => {
      ownerClosed = true
      // Deferred writes still need account-change protection after disposal.
      if (pendingOperations === 0)
        unsubscribe()
    })
  }

  // Register the read before its asynchronous result can change the store.
  // An account change invalidates this exact record and every deferred write.
  const ensureLoaded = (agentId: string) => {
    if (reads.has(agentId))
      return
    const record: StorageRead = { pending: null, touched: new Set() }
    reads.set(agentId, record)
    pendingOperations++
    record.pending = loadFromStorage(agentId).then((stored) => {
      if (reads.get(agentId) !== record)
        return new Set<SessionField>()
      const storedFields = new Set(Object.keys(stored) as SessionField[])
      if (storedFields.size > 0) {
        const remaining = { ...stored }
        // A clear also owns its field when the field was absent from memory.
        for (const field of record.touched)
          delete remaining[field]
        setState('infoByAgent', agentId, (previous = {}) => ({ ...remaining, ...previous }))
      }
      return storedFields
    }).catch(() => {
      // An unavailable stored row leaves the current in-memory state usable.
      return new Set<SessionField>()
    }).finally(() => {
      if (reads.get(agentId) === record) {
        record.pending = null
        record.touched.clear()
      }
      finishOperation()
    })
  }

  const whenLoaded = (agentId: string, body: (storedFields: ReadonlySet<SessionField>) => void) => {
    const record = reads.get(agentId)
    if (!record?.pending) {
      body(new Set())
      return
    }
    pendingOperations++
    void record.pending.then((storedFields) => {
      try {
        if (reads.get(agentId) === record)
          body(storedFields)
      }
      finally {
        finishOperation()
      }
    })
  }

  // Read at write time so storage receives the complete hydrated state.
  const persist = (agentId: string) => {
    whenLoaded(agentId, () => {
      const info = state.infoByAgent[agentId]
      if (info !== undefined)
        saveToStorage(agentId, info)
    })
  }

  const acceptsReplay = (agentId: string, replayId: bigint): boolean => {
    const receipt = replays.get(agentId)
    return receipt ? receipt.active && receipt.replayId === replayId : replayId === 0n
  }

  const claimFieldWrite = (agentId: string, field: ReceiptField, delivery?: SessionMetadataDelivery): boolean => {
    if (delivery?.phase === 'replay') {
      if (!acceptsReplay(agentId, delivery.replayId ?? 0n))
        return false
      const liveFields = replays.get(agentId)?.liveFields
      if (!liveFields?.has(field))
        return true
      const liveSeq = liveFields.get(field)
      return liveSeq !== null && liveSeq !== undefined && delivery.seq !== undefined && delivery.seq > liveSeq
    }
    let receipt = replays.get(agentId)
    if (!receipt) {
      // Generation zero supports direct handlers without a watch request.
      receipt = { replayId: 0n, active: true, liveFields: new Map() }
      replays.set(agentId, receipt)
    }
    if (receipt.active) {
      const seq = delivery?.seq !== undefined && delivery.seq > 0n ? delivery.seq : null
      const previous = receipt.liveFields.get(field)
      // An ephemeral update cannot become comparable through a later row.
      const latest = previous !== undefined && previous !== null && seq !== null && previous > seq ? previous : seq
      receipt.liveFields.set(field, previous === null ? null : latest)
    }
    return true
  }

  const touch = (agentId: string, field: SessionField) => {
    const read = reads.get(agentId)
    if (read?.pending)
      read.touched.add(field)
  }

  return {
    state,

    beginReplay(agentId: string, replayId: bigint) {
      if (replayId <= 0n || BigInt.asUintN(64, replayId) !== replayId)
        throw new Error('The replay request ID must be a positive uint64.')
      const current = replays.get(agentId)
      if (current && replayId <= current.replayId)
        return
      replays.set(agentId, { replayId, active: true, liveFields: new Map() })
    },

    retireReplay(agentId: string, replayId: bigint) {
      const receipt = replays.get(agentId)
      if (receipt?.replayId === replayId) {
        receipt.active = false
        receipt.liveFields.clear()
      }
    },

    removeReplay(agentId: string, replayId: bigint) {
      if (replays.get(agentId)?.replayId === replayId)
        replays.delete(agentId)
    },

    acceptsReplay,

    claimGoalProgressWrite(agentId: string, field: keyof GoalProgress, delivery: SessionMetadataDelivery): boolean {
      return claimFieldWrite(agentId, `goalProgress:${field}`, delivery)
    },

    claimGoalProgressClear(agentId: string, delivery: SessionMetadataDelivery): void {
      for (const field of Object.keys(GOAL_PROGRESS_FIELDS) as (keyof GoalProgress)[])
        claimFieldWrite(agentId, `goalProgress:${field}`, delivery)
    },

    getInfo(agentId: string): AgentSessionInfo {
      ensureLoaded(agentId)
      return state.infoByAgent[agentId] ?? {}
    },

    getProgress(agentId: string): LiveGenerationProgress {
      return state.progressByAgent[agentId] ?? { revision: 0 }
    },

    applyProgress(agentId: string, progress: LiveGenerationProgress) {
      const current = state.progressByAgent[agentId]
      if (current && progress.revision < current.revision)
        return
      setState('progressByAgent', agentId, reconcile(progress))
    },

    updateInfo(agentId: string, partial: Partial<AgentSessionInfo>, options: AgentSessionUpdateOptions = {}) {
      if (options.delivery?.phase === 'replay' && !acceptsReplay(agentId, options.delivery.replayId ?? 0n))
        return
      ensureLoaded(agentId)
      let shouldPersist = false
      setState('infoByAgent', agentId, (previous = {}) => {
        const merged = { ...previous }
        let changed = false
        for (const field of Object.keys(partial) as SessionField[]) {
          const value = partial[field]
          if (value === undefined || value === null || !claimFieldWrite(agentId, field, options.delivery))
            continue
          touch(agentId, field)
          if (field === 'rateLimits' && typeof value === 'object') {
            const incoming = value as Record<string, RateLimitInfo>
            const rateLimitOptions = options.rateLimits ?? { mode: 'merge' as const }
            const next = rateLimitOptions.mode === 'replace' ? {} : { ...merged.rateLimits }
            for (const [key, info] of Object.entries(incoming))
              next[key] = info
            for (const key of rateLimitOptions.deleteKeys ?? [])
              delete next[key]
            if (!rateLimitMapsEqual(merged.rateLimits, next)) {
              merged.rateLimits = next
              changed = true
            }
          }
          else if (!shallowEqual(merged[field], value)) {
            Object.assign(merged, { [field]: value })
            changed = true
          }
        }
        if (!changed)
          return previous
        shouldPersist = true
        return merged
      })
      if (shouldPersist)
        persist(agentId)
    },

    clearContextUsage(agentId: string, delivery?: SessionMetadataDelivery) {
      if (delivery?.phase === 'replay' && !acceptsReplay(agentId, delivery.replayId ?? 0n))
        return
      ensureLoaded(agentId)
      const fields = (['contextUsage', 'totalCostUsd'] as const).filter(field => claimFieldWrite(agentId, field, delivery))
      if (fields.length === 0)
        return
      const changed = fields.some(field => state.infoByAgent[agentId]?.[field] !== undefined)
      for (const field of fields)
        touch(agentId, field)
      if (state.infoByAgent[agentId] !== undefined) {
        if (fields.includes('contextUsage'))
          setState('infoByAgent', agentId, 'contextUsage', undefined)
        if (fields.includes('totalCostUsd'))
          setState('infoByAgent', agentId, 'totalCostUsd', undefined)
      }
      whenLoaded(agentId, (storedFields) => {
        // Persist an unseen stored clear, while leaving an absent clear unchanged.
        if (changed || fields.some(field => storedFields.has(field)))
          persist(agentId)
      })
    },

    clearThinkingTokens(agentId: string) {
      if (state.progressByAgent[agentId]?.thinkingTokens === undefined)
        return
      setState('progressByAgent', agentId, 'thinkingTokens', undefined)
    },

    clearOutputBytes(agentId: string) {
      if (state.progressByAgent[agentId]?.output === undefined)
        return
      setState('progressByAgent', agentId, 'output', undefined)
    },
  }
}
