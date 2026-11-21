import type { SoloBusyWork } from '~/api/platformBridge'
import { platformBridge } from '~/api/platformBridge'
import { createLogger } from '~/lib/logger'

const log = createLogger('soloStopConfirm')

export function isSoloBusyWorkEmpty(busy: SoloBusyWork): boolean {
  return busy.agents.length === 0 && busy.terminals.length === 0
}

/** Opens the switch-mode confirmation dialog and resolves with the user's choice. */
export type SoloSwitchModePrompter = (busy: SoloBusyWork) => Promise<boolean>

let prompter: SoloSwitchModePrompter | null = null
let inFlight: Promise<boolean> | null = null

export function setSoloSwitchModePrompter(fn: SoloSwitchModePrompter | null): void {
  prompter = fn
}

async function runConfirmSoloSwitchMode(): Promise<boolean> {
  const busy = await platformBridge.inspectSoloBusyWork()
  if (isSoloBusyWorkEmpty(busy))
    return true
  const currentPrompter = prompter
  if (!currentPrompter)
    return true
  try {
    return await currentPrompter(busy)
  }
  catch (err) {
    log.warn('switch-mode confirmation prompt failed; proceeding fail-open', err)
    return true
  }
}

export function confirmSoloSwitchMode(): Promise<boolean> {
  if (inFlight)
    return inFlight
  const pending = runConfirmSoloSwitchMode().finally(() => {
    inFlight = null
  })
  inFlight = pending
  return pending
}
