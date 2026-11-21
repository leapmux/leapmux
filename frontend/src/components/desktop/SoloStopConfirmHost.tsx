import type { Component } from 'solid-js'
import type { QuitConfirmEvent, SoloBusyWork } from '~/api/platformBridge'
import { createSignal, onCleanup, onMount, Show, untrack } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { SoloStopConfirmDialog } from '~/components/desktop/SoloStopConfirmDialog'
import { TAURI_EVENT_APP_CONFIRM_QUIT } from '~/generated/contracts/desktop'
import { createLogger } from '~/lib/logger'
import { setSoloSwitchModePrompter } from '~/lib/soloStopConfirm'

const log = createLogger('SoloStopConfirmHost')

type ActivePrompt
  = | { intent: 'quit', promptId: number, busyWork: SoloBusyWork }
    | { intent: 'switch-mode', busyWork: SoloBusyWork, resolve: (confirmed: boolean) => void }

function parseQuitConfirmEvent(raw: unknown): QuitConfirmEvent | null {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw))
    return null
  const record = raw as Record<string, unknown>
  if (typeof record.promptId !== 'number' || !Number.isFinite(record.promptId) || record.promptId <= 0)
    return null
  if (typeof record.busyWork !== 'object' || record.busyWork === null || Array.isArray(record.busyWork))
    return null
  const busy = record.busyWork as Record<string, unknown>
  if (!Array.isArray(busy.agents) || !Array.isArray(busy.terminals))
    return null
  return {
    promptId: record.promptId,
    busyWork: {
      agents: busy.agents,
      terminals: busy.terminals,
    },
  }
}

/**
 * App-root host for Solo-mode quit and switch-mode confirmation prompts.
 *
 * Mounts once outside the router and desktop state switch so quit events from
 * Rust are always acknowledged while the webview is alive, and owns modal
 * stacking above any already-open dialog.
 */
export const SoloStopConfirmHost: Component = () => {
  const [activePrompt, setActivePrompt] = createSignal<ActivePrompt | null>(null)

  let covered: Element[] = []
  let focusedBefore: HTMLElement | null = null
  let disposed = false
  let unlisten: (() => void) | null = null

  // Run before `setActivePrompt` renders this host's own dialog so the query
  // captures only dialogs beneath this prompt. Never re-run on in-place prompt
  // replacement (`switch-mode` -> `quit`), or this host's own dialog would be
  // marked inert.
  const cover = () => {
    focusedBefore = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null
    covered = [
      ...document.querySelectorAll('dialog[open]'),
    ]
    for (const dialog of covered)
      dialog.setAttribute('inert', '')
  }

  // Release `inert` before restoring focus: focus cannot move into an inert
  // subtree.
  const uncover = () => {
    for (const dialog of covered)
      dialog.removeAttribute('inert')
    covered = []
    const target = focusedBefore
    focusedBefore = null
    target?.focus()
  }

  const settle = (current: ActivePrompt, confirmed: boolean) => {
    if (untrack(activePrompt) !== current)
      return
    setActivePrompt(null)
    uncover()
    if (current.intent === 'quit')
      void platformBridge.resolveQuitConfirmation(current.promptId, confirmed)
    else
      current.resolve(confirmed)
  }

  const handleQuitEvent = async (raw: unknown) => {
    // Do not acknowledge after disposal: leaving Rust in `AwaitingAck` lets its
    // native fallback dialog open instead of silently cancelling the quit.
    if (disposed)
      return
    const parsed = parseQuitConfirmEvent(raw)
    if (!parsed)
      return
    const claimed = await platformBridge.ackQuitConfirmation(parsed.promptId)
    if (disposed) {
      if (claimed)
        void platformBridge.resolveQuitConfirmation(parsed.promptId, false)
      return
    }
    if (!claimed)
      return
    const existing = untrack(activePrompt)
    if (existing === null)
      cover()
    setActivePrompt({
      intent: 'quit',
      promptId: parsed.promptId,
      busyWork: parsed.busyWork,
    })
    if (existing?.intent === 'switch-mode')
      existing.resolve(false)
  }

  const teardown = () => {
    if (disposed)
      return
    disposed = true
    if (typeof window !== 'undefined')
      window.removeEventListener('pagehide', teardown)
    const stop = unlisten
    unlisten = null
    stop?.()
    setSoloSwitchModePrompter(null)
    const current = untrack(activePrompt)
    if (current)
      settle(current, false)
  }

  onMount(() => {
    window.addEventListener('pagehide', teardown)
    setSoloSwitchModePrompter((busyWork) => {
      if (disposed || untrack(activePrompt)?.intent === 'quit')
        return Promise.resolve(false)
      return new Promise<boolean>((resolve) => {
        const existing = untrack(activePrompt)
        if (existing === null)
          cover()
        setActivePrompt({
          intent: 'switch-mode',
          busyWork,
          resolve,
        })
        if (existing?.intent === 'switch-mode')
          existing.resolve(false)
      })
    })
    platformBridge.onEvent(TAURI_EVENT_APP_CONFIRM_QUIT, (payload) => {
      void handleQuitEvent(payload)
    })
      .then((stop) => {
        if (disposed) {
          stop()
          return
        }
        unlisten = stop
      })
      .catch((err) => {
        log.warn('failed to subscribe to app:confirm-quit', err)
      })
    onCleanup(teardown)
  })

  return (
    <Show keyed when={activePrompt()}>
      {current => (
        <SoloStopConfirmDialog
          intent={current.intent}
          busyWork={current.busyWork}
          onConfirm={() => settle(current, true)}
          onCancel={() => settle(current, false)}
        />
      )}
    </Show>
  )
}
