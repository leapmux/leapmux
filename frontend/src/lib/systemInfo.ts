import { authClient } from '~/api/clients'

let soloMode = false
let loaded = false

export async function loadSystemInfo(): Promise<void> {
  if (loaded)
    return
  try {
    const resp = await authClient.getSystemInfo({})
    soloMode = resp.soloMode
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
