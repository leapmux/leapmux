import type { ITheme } from '@xterm/xterm'
import type { Component } from 'solid-js'
import type { TerminalInstance } from '~/lib/terminal'
import type { Tab } from '~/stores/tab.store'
import { createEffect, createSignal, For, Match, onCleanup, onMount, Show, Switch } from 'solid-js'
import { StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'
import { usePreferences } from '~/context/PreferencesContext'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { isMac } from '~/lib/shortcuts/platform'
import { applyTerminalData, bufferHasVisibleContent, createTerminalInstance, reloadFontsAndClearAtlas, resolveTerminalTheme, resolveTerminalThemeMode } from '~/lib/terminal'
import * as styles from './TerminalView.css'
import '@xterm/xterm/css/xterm.css'

interface TerminalViewProps {
  terminals: Tab[]
  activeTerminalId: string | null
  visible: boolean
  onInput: (id: string, data: Uint8Array) => void
  onResize: (id: string, cols: number, rows: number) => void
  onTitleChange: (id: string, title: string) => void
  onBell: (id: string) => void
  /** Called once the terminal has painted any non-whitespace content. */
  onContentReady: (id: string) => void
  pageScrollRef?: (fn: (direction: -1 | 1) => void) => void
  writeRef?: (fn: (data: string) => void) => void
}

const instances = new Map<string, TerminalInstance>()
// Tracks which terminals have already had their initial screen snapshot
// applied. This must outlive TerminalContainer mount/unmount because the
// container re-mounts whenever its surrounding tile is restructured —
// e.g. converting a tile into a grid, swapping cells, or any other
// structural edit that changes a node's identity in the layout tree. A
// local-to-onMount latch would reset on every re-mount and re-apply the
// snapshot on top of live data.
const screenApplied = new Set<string>()
let lastActiveTerminalId: string | null = null

export function disposeTerminalInstance(id: string): void {
  const instance = instances.get(id)
  if (!instance)
    return
  instance.dispose()
  instances.delete(id)
  screenApplied.delete(id)
}

export function getTerminalInstance(id: string): TerminalInstance | undefined {
  return instances.get(id)
}

// During Vite HMR the module is re-evaluated, replacing `instances` with a
// fresh empty Map. Without this hook the previous Terminal objects (and
// their WebGL contexts, listeners, rAF callbacks) leak: nothing references
// them anymore but they're still wired into the DOM, and stray refresh
// callbacks fire against a renderer service that's mid-tear-down — which
// is the origin of `this._renderer.value.dimensions` crashes seen after
// HMR reloads.
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    for (const id of [...instances.keys()])
      disposeTerminalInstance(id)
    lastActiveTerminalId = null
  })
}

if (typeof window !== 'undefined') {
  (window as any).__getActiveTerminalText = () => {
    const instance = lastActiveTerminalId ? instances.get(lastActiveTerminalId) : undefined
    if (!instance)
      return ''
    const buffer = instance.terminal.buffer.active
    let text = ''
    for (let i = 0; i < buffer.length; i++) {
      const line = buffer.getLine(i)
      if (line)
        text += line.translateToString(true)
    }
    return text
  }
  ;(window as any).__getActiveTerminalRows = () => {
    return (lastActiveTerminalId ? instances.get(lastActiveTerminalId)?.terminal.rows : 0) ?? 0
  }
  ;(window as any).__getActiveTerminalBufferType = () => {
    const instance = lastActiveTerminalId ? instances.get(lastActiveTerminalId) : undefined
    return instance?.terminal.buffer.active.type ?? 'normal'
  }
}

/**
 * Per-terminal container. The xterm Terminal is constructed once per id
 * and stored in the module-level `instances` map; on re-mount the existing
 * Terminal's DOM element is re-parented into the new container ref rather
 * than calling `terminal.open()` again (which is no-op-on-second-call and
 * would leave the canvas detached in the previous, unmounted container).
 */
const TerminalContainer: Component<{
  terminalId: string
  active: boolean
  visible: boolean
  screen?: Uint8Array
  lastOffset?: number
  cols?: number
  rows?: number
  fontFamily: string
  fontSize: number
  theme: ITheme
  contentReady: boolean
  startupMessage?: string
  onInput: (id: string, data: Uint8Array) => void
  onResize: (id: string, cols: number, rows: number) => void
  onTitleChange: (id: string, title: string) => void
  onBell: (id: string) => void
  onContentReady: (id: string) => void
}> = (props) => {
  let ref: HTMLDivElement | undefined

  onMount(() => {
    if (!ref)
      return

    let instance = instances.get(props.terminalId)
    if (!instance) {
      instance = createTerminalInstance({
        fontFamily: props.fontFamily,
        fontSize: props.fontSize,
        cols: props.cols,
        rows: props.rows,
        theme: props.theme,
      })
      instances.set(props.terminalId, instance)

      const id = props.terminalId
      const onInput = props.onInput
      const onTitleChange = props.onTitleChange
      const onBell = props.onBell
      instance.sendInput = data => onInput(id, data)

      // On macOS, suppress CMD+Arrow and ALT+Arrow so xterm.js doesn't
      // process them — the shortcut system sends the correct escape sequences.
      if (isMac()) {
        instance.terminal.attachCustomKeyEventHandler((e: KeyboardEvent) => {
          if ((e.key === 'ArrowLeft' || e.key === 'ArrowRight') && (e.metaKey || e.altKey))
            return false
          return true
        })
      }

      instance.terminal.onData((data) => {
        if (!instances.get(id)?.suppressInput) {
          onInput(id, new TextEncoder().encode(data))
        }
      })
      instance.terminal.onTitleChange((title) => {
        onTitleChange(id, title)
      })
      instance.terminal.onBell(() => {
        if (!instances.get(id)?.suppressInput) {
          onBell(id)
        }
      })
    }

    // xterm's `Terminal.open()` is idempotent in a way that breaks
    // re-mount: on the second call it sees `this.element` already set and
    // early-returns without re-parenting it to the new container. The
    // xterm DOM stays inside the previous (now-unmounted) ref, the new
    // ref is empty, and the canvas ends up detached from the document.
    // Re-parent the existing element ourselves when we know the instance
    // was already opened (any layout edit that changes a tile's identity
    // in the layout tree will remount its TerminalContainer below).
    if (instance.terminal.element && instance.terminal.element.parentElement !== ref)
      ref.appendChild(instance.terminal.element)
    else
      instance.terminal.open(ref)

    // Write screen snapshot if available (restore on refresh). Seed the
    // resume cursor from lastOffset (from the backend, carried through
    // the tab store) rather than screen.length — once the backend's ring
    // has wrapped they differ, and the offset is what the backend uses
    // to compute the catch-up delta on resubscribe.
    //
    // The screen may also arrive *after* mount when ListTerminals is
    // queued behind a worker reconnect, so a reactive effect applies it
    // the first time it becomes non-empty. The `screenApplied` set
    // latches per instance (not per mount) so subsequent reactive prop
    // changes — including remounts driven by layout restructuring —
    // don't re-apply the same snapshot on top of live data.
    createEffect(() => {
      if (screenApplied.has(props.terminalId))
        return
      const screen = props.screen
      if (!screen || screen.length === 0)
        return
      screenApplied.add(props.terminalId)
      const termId = props.terminalId
      const reportReady = props.onContentReady
      applyTerminalData(
        instance,
        screen,
        true,
        props.lastOffset ?? screen.length,
        0,
        () => {
          if (bufferHasVisibleContent(instance.terminal))
            reportReady(termId)
        },
      )
    })

    // ResizeObserver on this terminal's container element.
    // Only send resize to worker when dimensions actually change to avoid
    // unnecessary SIGWINCH that triggers zsh PROMPT_SP '%' on snapshot restore.
    const resizeObserver = new ResizeObserver(() => {
      const inst = instances.get(props.terminalId)
      if (inst && props.active && props.visible) {
        const prevCols = inst.terminal.cols
        const prevRows = inst.terminal.rows
        inst.fitAddon.fit()
        if (inst.terminal.cols !== prevCols || inst.terminal.rows !== prevRows) {
          props.onResize(props.terminalId, inst.terminal.cols, inst.terminal.rows)
        }
      }
    })
    resizeObserver.observe(ref)

    onCleanup(() => {
      resizeObserver.disconnect()
    })
  })

  // Re-fit when this terminal becomes active+visible
  createEffect(() => {
    if (props.active && props.visible) {
      const instance = instances.get(props.terminalId)
      if (instance) {
        requestAnimationFrame(() => {
          const prevCols = instance.terminal.cols
          const prevRows = instance.terminal.rows
          instance.fitAddon.fit()
          instance.terminal.focus()
          if (instance.terminal.cols !== prevCols || instance.terminal.rows !== prevRows) {
            props.onResize(props.terminalId, instance.terminal.cols, instance.terminal.rows)
          }
        })
      }
    }
  })

  return (
    <div
      class={styles.terminalWrapper}
      classList={{ [styles.terminalWrapperHidden]: !props.active }}
      data-terminal-id={props.terminalId}
      data-active={props.active ? 'true' : 'false'}
    >
      <div ref={ref} class={styles.xtermHost} />
      <Show when={!props.contentReady}>
        <div class={styles.startupOverlay} data-testid="terminal-startup-overlay">
          <StartupSpinner label={props.startupMessage || 'Starting terminal…'} />
        </div>
      </Show>
    </div>
  )
}

export const TerminalView: Component<TerminalViewProps> = (props) => {
  const preferences = usePreferences()

  const pageScroll = (direction: -1 | 1) => {
    if (!props.activeTerminalId)
      return
    instances.get(props.activeTerminalId)?.terminal.scrollPages(direction)
  }

  const write = (data: string) => {
    if (!props.activeTerminalId)
      return
    const instance = instances.get(props.activeTerminalId)
    if (instance?.sendInput)
      instance.sendInput(new TextEncoder().encode(data))
  }

  // React to font preference changes and update existing terminal instances.
  // After the new family's variants finish loading, clear each terminal's
  // atlas so the WebGL renderer drops any fallback glyphs it rasterized
  // before the swap.
  createEffect(() => {
    const family = preferences.monoFontFamily()
    for (const [, instance] of instances) {
      instance.terminal.options.fontFamily = family
      instance.fitAddon.fit()
      reloadFontsAndClearAtlas(instance.terminal, family, 13)
    }
  })

  // Track OS-level prefers-color-scheme reactively so terminal theme can
  // follow the system when both UI theme is `system` and terminal theme
  // is `match-ui`. Without this, flipping macOS dark mode would not
  // propagate to live xterm instances.
  const [prefersDark, setPrefersDark] = createSignal(
    typeof window !== 'undefined'
    && window.matchMedia('(prefers-color-scheme: dark)').matches,
  )
  onMount(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const handler = (e: MediaQueryListEvent) => setPrefersDark(e.matches)
    mq.addEventListener('change', handler)
    onCleanup(() => mq.removeEventListener('change', handler))
  })

  // React to terminal/UI theme preference and OS-theme changes — all
  // three feed into the resolved theme when the user picks `match-ui`.
  createEffect(() => {
    const theme = resolveTerminalTheme(
      preferences.terminalTheme(),
      preferences.theme(),
      prefersDark(),
    )
    for (const [, instance] of instances) {
      instance.terminal.options.theme = theme
    }
  })

  createEffect(() => {
    lastActiveTerminalId = props.activeTerminalId
    props.pageScrollRef?.(pageScroll)
    props.writeRef?.(write)
  })

  // Per-view ownership of terminal ids. The `instances` map is module-
  // level and shared by every mounted TerminalView (one per tile). Each
  // view tracks the ids it has rendered so it can dispose them on
  // unmount without nuking instances owned by sibling views (which would
  // happen if the dispose effect scanned the global `instances` map).
  //
  // Disposal of explicitly closed terminals is owned by
  // useTerminalOperations.handleTerminalClose, which calls
  // disposeTerminalInstance directly. This effect only releases ids
  // from this view's ownership set as they leave `props.terminals` — no
  // dispose, because the id may have moved to another tile (where the
  // sibling view will re-parent the existing xterm element).
  const ownedIds = new Set<string>()
  createEffect(() => {
    const currentIds = new Set(props.terminals.map(t => t.id))
    for (const id of currentIds)
      ownedIds.add(id)
    for (const id of [...ownedIds]) {
      if (!currentIds.has(id))
        ownedIds.delete(id)
    }
  })

  // On unmount (e.g. workspace switch, tile becomes empty), dispose
  // any terminals this view still owns — but defer to a microtask so a
  // sibling TerminalView that just mounted to take over the same id
  // (tab moved between tiles) gets first crack at re-parenting the
  // xterm element. We only dispose if the element is no longer attached
  // anywhere.
  onCleanup(() => {
    const toCheck = [...ownedIds]
    ownedIds.clear()
    queueMicrotask(() => {
      for (const id of toCheck) {
        const inst = instances.get(id)
        if (inst && !inst.terminal.element?.isConnected)
          disposeTerminalInstance(id)
      }
    })
  })

  const terminalTheme = () => resolveTerminalTheme(
    preferences.terminalTheme(),
    preferences.theme(),
    prefersDark(),
  )
  const terminalThemeMode = () => resolveTerminalThemeMode(
    preferences.terminalTheme(),
    preferences.theme(),
    prefersDark(),
  )

  return (
    <div class={styles.container} data-theme={terminalThemeMode()}>
      <div class={styles.terminalInner}>
        <For each={props.terminals}>
          {terminal => (
            <Switch
              fallback={(
                <TerminalContainer
                  terminalId={terminal.id}
                  active={terminal.id === props.activeTerminalId}
                  visible={props.visible}
                  screen={terminal.screen}
                  lastOffset={terminal.lastOffset}
                  cols={terminal.cols}
                  rows={terminal.rows}
                  fontFamily={preferences.monoFontFamily()}
                  fontSize={13}
                  theme={terminalTheme()}
                  contentReady={terminal.contentReady ?? false}
                  startupMessage={terminal.startupMessage}
                  onInput={props.onInput}
                  onResize={props.onResize}
                  onTitleChange={props.onTitleChange}
                  onBell={props.onBell}
                  onContentReady={props.onContentReady}
                />
              )}
            >
              <Match when={terminal.status === TerminalStatus.STARTUP_FAILED}>
                <div
                  class={styles.startupErrorPane}
                  classList={{ [styles.terminalWrapperHidden]: terminal.id !== props.activeTerminalId }}
                  data-testid="terminal-startup-error"
                >
                  <StartupErrorBody
                    title="Terminal failed to start"
                    error={terminal.startupError ?? ''}
                  />
                </div>
              </Match>
            </Switch>
          )}
        </For>
      </div>
    </div>
  )
}
