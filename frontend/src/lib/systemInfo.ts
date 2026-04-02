import { authClient } from '~/api/clients'

export interface BuildInfo {
  version: string
  commitHash: string
  buildTime: string
}

let soloMode = false
let loaded = false

let backendBuildInfo: BuildInfo = { version: '', commitHash: '', buildTime: '' }

const frontendBuildInfo: BuildInfo = {
  version: import.meta.env.LEAPMUX_VERSION || '',
  commitHash: import.meta.env.LEAPMUX_COMMIT_HASH || '',
  buildTime: import.meta.env.LEAPMUX_BUILD_TIME || '',
}

export async function loadSystemInfo(): Promise<void> {
  if (loaded)
    return
  try {
    const resp = await authClient.getSystemInfo({})
    soloMode = resp.soloMode
    backendBuildInfo = {
      version: resp.version,
      commitHash: resp.commitHash,
      buildTime: resp.buildTime,
    }
    loaded = true
  }
  catch {
    // Ignore — defaults to false (non-solo)
  }
}

export function isSoloMode(): boolean {
  return soloMode
}

export function isDesktopApp(): boolean {
  return typeof (window as any).__lm_switchMode === 'function'
}

export function getBackendBuildInfo(): BuildInfo {
  return backendBuildInfo
}

export function getFrontendBuildInfo(): BuildInfo {
  return frontendBuildInfo
}
