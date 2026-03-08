/**
 * localStorage cache for worker system info fetched via E2EE.
 * Persists across page reloads so offline workers still show last-known info.
 */

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
  try {
    const raw = localStorage.getItem(KEY_PREFIX + workerId)
    if (!raw)
      return null
    return JSON.parse(raw) as WorkerInfo
  }
  catch {
    return null
  }
}

export function setWorkerInfo(workerId: string, info: WorkerInfo): void {
  try {
    localStorage.setItem(KEY_PREFIX + workerId, JSON.stringify(info))
  }
  catch {
    // localStorage full or unavailable — silently ignore.
  }
}

export function clearWorkerInfo(workerId: string): void {
  localStorage.removeItem(KEY_PREFIX + workerId)
}
