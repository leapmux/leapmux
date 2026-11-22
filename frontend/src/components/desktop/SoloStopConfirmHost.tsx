import type { Component } from 'solid-js'
import type { QuitConfirmEvent, SoloBusyWork } from '~/api/platformBridge'
import { createSignal, onCleanup, onMount, Show, untrack } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { createModalCover } from '~/components/common/modalCover'
import { SoloStopConfirmDialog } from '~/components/desktop/SoloStopConfirmDialog'
import { createLogger } from '~/lib/logger'
import { setSoloSwitchModePrompter } from '~/lib/soloStopConfirm'

const log = createLogger('SoloStopConfirmHost')

type ActivePrompt
  = | { intent: 'quit', promptId: number, busyWork: SoloBusyWork }
    | { intent: 'switch-mode', busyWork: SoloBusyWork, resolve: (confirmed: boolean) => void }

/**
 * App-root host for Solo-mode quit and switch-mode confirmation prompts.
 *
 * Mounts once outside the router and desktop state switch so quit events from
 * Rust are always acknowledged while the webview is alive, and owns modal
 * stacking above any already-open dialog.
 */
export const SoloStopConfirmHost: Component = () => {
  const [activePrompt, setActivePrompt] = createSignal<ActivePrompt | null>(null)

  const modalCover = createModalCover()
  let disposed = false
  let unlisten: (() => void) | null = null

  const settle = (current: ActivePrompt, confirmed: boolean) => {
    if (untrack(activePrompt) !== current)
      return
    setActivePrompt(null)
    modalCover.uncover()
    if (current.intent === 'quit')
      void platformBridge.resolveQuitConfirmation(current.promptId, confirmed)
    else
      current.resolve(confirmed)
  }

  const handleQuitEvent = async (event: QuitConfirmEvent) => {
    // Do not acknowledge after disposal: leaving Rust in `AwaitingAck` lets its
    // native fallback dialog open instead of silently cancelling the quit.
    if (disposed)
      return
    const claimed = await platformBridge.ackQuitConfirmation(event.promptId)
    if (disposed) {
      if (claimed)
        void platformBridge.resolveQuitConfirmation(event.promptId, false)
      return
    }
    if (!claimed)
      return
    const existing = untrack(activePrompt)
    modalCover.cover()
    setActivePrompt({
      intent: 'quit',
      promptId: event.promptId,
      busyWork: event.busyWork,
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
        modalCover.cover()
        setActivePrompt({
          intent: 'switch-mode',
          busyWork,
          resolve,
        })
        if (existing?.intent === 'switch-mode')
          existing.resolve(false)
      })
    })
    platformBridge.onQuitConfirmation((event) => {
      void handleQuitEvent(event)
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
