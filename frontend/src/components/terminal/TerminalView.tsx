import type { Component } from 'solid-js'
import type { TerminalInstance } from '~/lib/terminal'
import type { TerminalInfo } from '~/stores/terminal.store'
import { createEffect, For, onCleanup, onMount } from 'solid-js'
import { usePreferences } from '~/context/PreferencesContext'
import { createTerminalInstance } from '~/lib/terminal'
import * as styles from './TerminalView.css'
import '@xterm/xterm/css/xterm.css'

interface TerminalViewProps {
  terminals: TerminalInfo[]
  activeTerminalId: string | null
  visible: boolean
  onInput: (id: string, data: Uint8Array) => void
  onResize: (id: string, cols: number, rows: number) => void
  onTitleChange: (id: string, title: string) => void
  onBell: (id: string) => void
}

const instances = new Map<string, TerminalInstance>()

export function getTerminalInstance(id: string): TerminalInstance | undefined {
  return instances.get(id)
}

// Expose terminal text reader for E2E tests (WebGL renderer makes DOM rows empty)
if (typeof window !== 'undefined') {
  (window as any).__getActiveTerminalText = () => {
    for (const [id, instance] of instances) {
      const container = document.querySelector(`[data-terminal-id="${id}"]`) as HTMLElement | null
      if (container && container.style.display !== 'none') {
        const buffer = instance.terminal.buffer.active
        let text = ''
        for (let i = 0; i < buffer.length; i++) {
          const line = buffer.getLine(i)
          if (line) {
            text += line.translateToString(true)
          }
        }
        return text
      }
    }
    return ''
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
  onInput: (id: string, data: Uint8Array) => void
  onResize: (id: string, cols: number, rows: number) => void
  onTitleChange: (id: string, title: string) => void
  onBell: (id: string) => void
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
      })
      instances.set(props.terminalId, instance)

      const id = props.terminalId
      const onInput = props.onInput
      const onTitleChange = props.onTitleChange
      const onBell = props.onBell
      instance.terminal.onData((data) => {
        onInput(id, new TextEncoder().encode(data))
      })
      instance.terminal.onTitleChange((title) => {
        onTitleChange(id, title)
      })
      instance.terminal.onBell(() => {
        onBell(id)
      })
    }

    instance.terminal.open(ref)

    // Write screen snapshot if available (restore on refresh)
    if (props.screen && props.screen.length > 0) {
      instance.terminal.write(props.screen)
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
      ref={ref}
      class={styles.terminalWrapper}
      data-terminal-id={props.terminalId}
      style={{ display: props.active ? undefined : 'none' }}
    />
  )
}

export const TerminalView: Component<TerminalViewProps> = (props) => {
  const preferences = usePreferences()

  // React to font preference changes and update existing terminal instances
  createEffect(() => {
    const family = preferences.monoFontFamily()
    for (const [, instance] of instances) {
      instance.terminal.options.fontFamily = family
      instance.fitAddon.fit()
    }
  })

  // Clean up instances when component unmounts
  onCleanup(() => {
    for (const [, instance] of instances) {
      instance.dispose()
    }
    instances.clear()
  })

  return (
    <div class={styles.container}>
      <div class={styles.terminalInner}>
        <For each={props.terminals}>
          {terminal => (
            <TerminalContainer
              terminalId={terminal.id}
              active={terminal.id === props.activeTerminalId}
              visible={props.visible}
              screen={terminal.screen}
              cols={terminal.cols}
              rows={terminal.rows}
              fontFamily={preferences.monoFontFamily()}
              fontSize={13}
              onInput={props.onInput}
              onResize={props.onResize}
              onTitleChange={props.onTitleChange}
              onBell={props.onBell}
            />
          )}
        </For>
      </div>
    </div>
  )
}
