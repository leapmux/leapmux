/**
 * Persistent cache for worker system info fetched via E2EE.
 * Survives page reloads so offline workers still show last-known info.
 *
 * Reads are ASYNCHRONOUS: this is an unbounded family (one row per worker the
 * user has ever reached) on the unmirrored storage tier. `workerInfo.store`
 * holds the synchronous view its reactive readers need.
 */

import { localStorageDrop, localStorageLoad, localStorageStore, PREFIX_WORKER_INFO } from './browserStorage'

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

export async function getWorkerInfo(workerId: string): Promise<WorkerInfo | null> {
  return (await localStorageLoad<WorkerInfo>(`${PREFIX_WORKER_INFO}${workerId}`)) ?? null
}

export function setWorkerInfo(workerId: string, info: WorkerInfo): void {
  localStorageStore(`${PREFIX_WORKER_INFO}${workerId}`, info)
}

export function clearWorkerInfo(workerId: string): void {
  localStorageDrop(`${PREFIX_WORKER_INFO}${workerId}`)
}
