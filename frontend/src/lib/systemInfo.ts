import { authClient } from '~/api/clients'

export interface BuildInfo {
  version: string
  commitHash: string
  commitTime: string
  buildTime: string
}

let soloMode = false
let loaded = false

let backendBuildInfo: BuildInfo = { version: '', commitHash: '', commitTime: '', buildTime: '' }

const frontendBuildInfo: BuildInfo = {
  version: import.meta.env.LEAPMUX_VERSION || '',
  commitHash: import.meta.env.LEAPMUX_COMMIT_HASH || '',
  commitTime: import.meta.env.LEAPMUX_COMMIT_TIME || '',
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
      commitTime: resp.commitTime,
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

const logoColor = '#0D9488'

const logoArt = [
  '  █   █▀▀ █▀█ █▀█ █▄ ▄█ █ █ █ █',
  '  █   █▀  █▀█ █▀▀ █ ▀ █ █ █ ▄▀▄',
  '  ▀▀▀ ▀▀▀ ▀ ▀ ▀   ▀   ▀ ▀▀▀ ▀ ▀',
].map(l => l.replaceAll(' ', '\u2007'))

export function formatBuildTime(iso: string): string {
  if (!iso)
    return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()))
    return iso
  return d.toLocaleString(undefined, {
    weekday: 'short',
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    second: '2-digit',
  })
}

export function formatVersionLine(info: BuildInfo): string {
  let line = info.version || 'dev'
  if (info.commitHash)
    line += ` (${info.commitHash})`
  const time = formatBuildTime(info.buildTime)
  if (time)
    line += ` \u00B7 ${time}`
  return line
}

let bannerPrinted = false

export function printConsoleBanner(): void {
  if (bannerPrinted)
    return
  bannerPrinted = true

  const backend = backendBuildInfo
  const frontend = frontendBuildInfo
  const same = formatVersionLine(backend) === formatVersionLine(frontend)

  // Build styled console.log arguments.
  // Each art line: logo portion in teal, then reset.
  const lines = logoArt.map(l => `%c${l}%c`)
  const styles = logoArt.flatMap(() => [`color:${logoColor};font-weight:bold`, ''])

  // Version info below the art.
  if (same) {
    lines.push(`  ${formatVersionLine(backend)}`)
  }
  else {
    lines.push(`  Backend:  ${formatVersionLine(backend)}`)
    lines.push(`  Frontend: ${formatVersionLine(frontend)}`)
  }
  const year = backend.commitTime ? new Date(backend.commitTime).getFullYear() : new Date().getFullYear()
  lines.push(`  Copyright \u00A9 ${year} Event Loop, Inc.`)

  // eslint-disable-next-line no-console
  console.log(lines.join('\n'), ...styles)
}
