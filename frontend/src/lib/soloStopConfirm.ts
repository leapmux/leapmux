import type { SoloBusyWork } from '~/api/platformBridge'
import { platformBridge } from '~/api/platformBridge'
import { createLogger } from '~/lib/logger'
import { motion } from '~/styles/tokens'

const log = createLogger('soloStopConfirm')

export function isSoloBusyWorkEmpty(busy: SoloBusyWork): boolean {
  return busy.agents.length === 0 && busy.terminals.length === 0
}

/** Opens the switch-mode confirmation dialog and resolves with the user's choice. */
export type SoloSwitchModePrompter = (busy: SoloBusyWork) => Promise<boolean>

let prompter: SoloSwitchModePrompter | null = null
let inFlight: Promise<number | null> | null = null
let switchTransitionInFlight: Promise<void> | null = null

export function setSoloSwitchModePrompter(fn: SoloSwitchModePrompter | null): void {
  prompter = fn
}

async function runConfirmSoloSwitchMode(): Promise<number | null> {
  const started = await platformBridge.beginSwitchMode()
  if (started.kind === 'blocked')
    return null
  if (started.kind === 'proceed')
    return started.switchId
  const currentPrompter = prompter
  if (!currentPrompter)
    return platformBridge.resolveSwitchModeConfirmation(started.promptId, true)
  let confirmed: boolean
  try {
    confirmed = await currentPrompter(started.busyWork)
  }
  catch (err) {
    log.warn('switch-mode confirmation prompt failed; proceeding fail-open', err)
    confirmed = true
  }
  return platformBridge.resolveSwitchModeConfirmation(started.promptId, confirmed)
}

export function confirmSoloSwitchMode(): Promise<number | null> {
  if (inFlight)
    return inFlight
  const pending = runConfirmSoloSwitchMode().finally(() => {
    inFlight = null
  })
  inFlight = pending
  return pending
}

async function runDesktopSwitchMode(): Promise<void> {
  const switchId = await confirmSoloSwitchMode()
  if (switchId === null)
    return

  let handedToSwitchMode = false
  const overlay = document.createElement('div')
  const bg = getComputedStyle(document.documentElement).getPropertyValue('--background').trim() || '#000'
  overlay.style.cssText = `position:fixed;inset:0;z-index:2147483647;background:${bg};opacity:0;transition:opacity var(--transition)`
  document.body.appendChild(overlay)
  try {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        overlay.style.opacity = '1'
      })
      const fallback = setTimeout(resolve, motion.medium + 100)
      overlay.addEventListener('transitionend', () => {
        clearTimeout(fallback)
        resolve()
      }, { once: true })
    })

    handedToSwitchMode = true
    await platformBridge.switchMode(switchId)
  }
  catch (err) {
    if (!handedToSwitchMode)
      await platformBridge.cancelSwitchMode(switchId)
    log.warn('switch mode failed', err)
    return
  }
  finally {
    overlay.remove()
  }
  window.__leapmux_disconnectDesktop?.()
}

/**
 * Runs the desktop Switch mode workflow behind one module-level single-flight
 * guard so multiple or remounted `UserMenuItems` instances cannot trigger
 * duplicate fade overlays or concurrent `switchMode()` calls.
 */
export function requestDesktopSwitchMode(): Promise<void> {
  if (switchTransitionInFlight)
    return switchTransitionInFlight
  const pending = runDesktopSwitchMode().finally(() => {
    switchTransitionInFlight = null
  })
  switchTransitionInFlight = pending
  return pending
}
