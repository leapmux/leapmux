import type { ITheme } from '@xterm/xterm'
import type { Component } from 'solid-js'
import type { TerminalInstance } from '~/lib/terminal'
import type { Tab } from '~/stores/tab.store'
import { createEffect, For, Match, onCleanup, onMount, Show, Switch } from 'solid-js'
import { StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'
import { usePreferences } from '~/context/PreferencesContext'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { isMac } from '~/lib/shortcuts/platform'
import { bufferHasVisibleContent, createTerminalInstance, resolveTerminalTheme, resolveTerminalThemeMode } from '~/lib/terminal'
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
}

/** Per-terminal container that calls terminal.open() exactly once. */
const TerminalContainer: Component<{
  terminalId: string
  active: boolean
  visible: boolean
  screen?: Uint8Array
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

    // Write screen snapshot if available (restore on refresh).
    // Suppress onData during replay to prevent xterm.js query responses
    // (DECRPM, DA, DECRQSS, OSC) from being forwarded to the PTY,
    // where the shell's echo would display them as visible text.
    if (props.screen && props.screen.length > 0) {
      const termId = props.terminalId
      const reportReady = props.onContentReady
      instance.suppressInput = true
      instance.terminal.write(props.screen, () => {
        instance!.suppressInput = false
        if (bufferHasVisibleContent(instance!.terminal))
          reportReady(termId)
      })
      instance.screenRestored = true
    }

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
      data-terminal-id={props.terminalId}
      style={{ display: props.active ? undefined : 'none' }}
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
                  data-testid="terminal-startup-error"
                  style={{ display: terminal.id === props.activeTerminalId ? 'flex' : 'none' }}
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
