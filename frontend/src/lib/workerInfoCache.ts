/**
 * localStorage cache for worker system info fetched via E2EE.
 * Persists across page reloads so offline workers still show last-known info.
 */

import { localStorageGet, localStorageRemove, localStorageSet, PREFIX_WORKER_INFO } from './browserStorage'

export interface WorkerInfo {
  name: string
  os: string
  arch: string
  homeDir: string
  version: string
  commitHash: string
  buildTime: string
  updatedAt: number // Date.now()
}

export function getWorkerInfo(workerId: string): WorkerInfo | null {
  return localStorageGet<WorkerInfo>(PREFIX_WORKER_INFO + workerId) ?? null
}

export function setWorkerInfo(workerId: string, info: WorkerInfo): void {
  localStorageSet(PREFIX_WORKER_INFO + workerId, info)
}

export function clearWorkerInfo(workerId: string): void {
  localStorageRemove(PREFIX_WORKER_INFO + workerId)
}
