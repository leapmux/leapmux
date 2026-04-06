/**
 * localStorage cache for worker system info fetched via E2EE.
 * Persists across page reloads so offline workers still show last-known info.
 */

import { safeGetJson, safeRemoveItem, safeSetJson } from './safeStorage'
import { PREFIX_WORKER_INFO } from './storageCleanup'

export interface WorkerInfo {
  name: string
  os: string
  arch: string
  homeDir: string
  version: string
  updatedAt: number // Date.now()
}

const KEY_PREFIX = PREFIX_WORKER_INFO

export function getWorkerInfo(workerId: string): WorkerInfo | null {
  return safeGetJson<WorkerInfo>(KEY_PREFIX + workerId) ?? null
}

export function setWorkerInfo(workerId: string, info: WorkerInfo): void {
  safeSetJson(KEY_PREFIX + workerId, info)
}

export function clearWorkerInfo(workerId: string): void {
  safeRemoveItem(KEY_PREFIX + workerId)
}
