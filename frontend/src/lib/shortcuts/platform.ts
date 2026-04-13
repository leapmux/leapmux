import type { Platform } from './types'

const MAC_RE = /Mac|iPod|iPhone|iPad/
const WIN_RE = /Windows/

let cachedPlatform: Platform | undefined

export function getPlatform(): Platform {
  if (cachedPlatform)
    return cachedPlatform

  const ua = typeof navigator !== 'undefined' ? navigator.userAgent : ''
  if (MAC_RE.test(ua)) {
    cachedPlatform = 'mac'
  }
  else if (WIN_RE.test(ua)) {
    cachedPlatform = 'windows'
  }
  else {
    cachedPlatform = 'linux'
  }
  return cachedPlatform
}

export function isMac(): boolean {
  return getPlatform() === 'mac'
}
