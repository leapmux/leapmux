import type { ITheme } from '@xterm/xterm'
import type { Component } from 'solid-js'
import type { TerminalInstance } from '~/lib/terminal'
import type { Tab } from '~/stores/tab.store'
import { createEffect, For, Match, onCleanup, onMount, Show, Switch } from 'solid-js'
import { StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'
import { usePreferences } from '~/context/PreferencesContext'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { isMac } from '~/lib/shortcuts/platform'
import { applyTerminalData, bufferHasVisibleContent, createTerminalInstance, resolveTerminalTheme, resolveTerminalThemeMode } from '~/lib/terminal'
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
let lastActiveTerminalId: string | null = null

export function getTerminalInstance(id: string): TerminalInstance | undefined {
  return instances.get(id)
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

/** Per-terminal container that calls terminal.open() exactly once. */
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

    instance.terminal.open(ref)

    // Write screen snapshot if available (restore on refresh). Seed the
    // resume cursor from lastOffset (from the backend, carried through
    // the tab store) rather than screen.length — once the backend's ring
    // has wrapped they differ, and the offset is what the backend uses
    // to compute the catch-up delta on resubscribe.
    //
    // The screen may also arrive *after* mount when ListTerminals is
    // queued behind a worker reconnect, so a reactive effect applies it
    // the first time it becomes non-empty. `applied` latches per
    // instance so subsequent reactive prop changes don't re-apply the
    // same snapshot on top of live data.
    let applied = false
    createEffect(() => {
      if (applied)
        return
      const screen = props.screen
      if (!screen || screen.length === 0)
        return
      applied = true
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

  // React to font preference changes and update existing terminal instances
  createEffect(() => {
    const family = preferences.monoFontFamily()
    for (const [, instance] of instances) {
      instance.terminal.options.fontFamily = family
      instance.fitAddon.fit()
    }
  })

  // React to terminal theme preference changes
  createEffect(() => {
    const theme = resolveTerminalTheme(preferences.terminalTheme())
    for (const [, instance] of instances) {
      instance.terminal.options.theme = theme
    }
  })

  createEffect(() => {
    lastActiveTerminalId = props.activeTerminalId
    props.pageScrollRef?.(pageScroll)
    props.writeRef?.(write)
  })

  // Dispose per-terminal instances as tabs are closed. The `instances`
  // map deliberately outlives TerminalContainer mount/unmount (status
  // transitions like STARTING → STARTUP_FAILED → READY swap between the
  // container and the error pane while keeping the xterm alive), so this
  // is the authoritative place to release WebGL contexts and listener
  // refs for removed terminals.
  createEffect(() => {
    const liveIds = new Set(props.terminals.map(t => t.id))
    for (const id of [...instances.keys()]) {
      if (!liveIds.has(id)) {
        instances.get(id)?.dispose()
        instances.delete(id)
      }
    }
  })

  // Clean up instances when component unmounts
  onCleanup(() => {
    for (const [, instance] of instances) {
      instance.dispose()
    }
    instances.clear()
  })

  const terminalTheme = () => resolveTerminalTheme(preferences.terminalTheme())
  const terminalThemeMode = () => resolveTerminalThemeMode(preferences.terminalTheme())

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
