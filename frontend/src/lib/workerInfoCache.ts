/**
 * localStorage cache for worker system info fetched via E2EE.
 * Persists across page reloads so offline workers still show last-known info.
 */

import { safeGetJson, safeRemoveItem, safeSetJson } from './safeStorage'

export interface WorkerInfo {
  name: string
  os: string
  arch: string
  homeDir: string
  version: string
  updatedAt: number // Date.now()
}

const KEY_PREFIX = 'leapmux:worker-info:'

export function getWorkerInfo(workerId: string): WorkerInfo | null {
  return safeGetJson<WorkerInfo>(KEY_PREFIX + workerId) ?? null
}

export function setWorkerInfo(workerId: string, info: WorkerInfo): void {
  safeSetJson(KEY_PREFIX + workerId, info)
}

export function clearWorkerInfo(workerId: string): void {
  safeRemoveItem(KEY_PREFIX + workerId)
}
