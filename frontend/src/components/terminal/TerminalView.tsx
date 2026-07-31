import type { ITheme } from '@xterm/xterm'
import type { Component } from 'solid-js'
import type { TerminalInstance } from '~/lib/terminal'
import type { TerminalTab } from '~/stores/tab.types'
import { createEffect, createMemo, createSignal, For, Match, onCleanup, onMount, Show, Switch } from 'solid-js'
import { StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'
import { usePreferences } from '~/context/PreferencesContext'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { createKeyedRows } from '~/lib/keyedRows'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { isMac } from '~/lib/shortcuts/platform'
import { applyTerminalData, bufferHasVisibleContent, createTerminalInstance, DEFAULT_FONT_SIZE, refreshTerminalFont, resolveTerminalTheme, resolveTerminalThemeMode, serializeXtermBuffer } from '~/lib/terminal'
import { webglPool } from '~/lib/webglTerminalPool'
import * as styles from './TerminalView.css'
import '@xterm/xterm/css/xterm.css'

interface TerminalViewProps {
  terminals: TerminalTab[]
  activeTerminalId: string | null
  visible: boolean
  /** Whether the enclosing tile is the layout-focused tile. See TerminalContainer.tileFocused. */
  tileFocused: boolean
  /**
   * Resume cursor for a terminal, read at mount to seed its snapshot apply.
   *
   * A lookup rather than a field on `TerminalTab`, because it is written at
   * PTY-read frequency and `tabView`'s join subscribes to every metadata field
   * it reads -- carrying it on the tab made a busy terminal re-run the
   * account-wide join per output chunk. See `TerminalMeta.lastOffset`.
   */
  getLastOffset?: (id: string) => number | undefined
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

/**
 * Where a disposing terminal's scrollback goes.
 *
 * Registered once by `AppShell` (the same module-level sink pattern as
 * `setCRDTBridge` / `setExpectedUserId`), because this module must not depend
 * on the store graph. Left unset in tests that render a `TerminalView` without
 * a metadata store, where dropping the buffer is the correct behaviour.
 */
let screenSink: ((tabId: string, screen: Uint8Array) => void) | null = null

export function setTerminalScreenSink(sink: ((tabId: string, screen: Uint8Array) => void) | null): void {
  screenSink = sink
}

/**
 * Tear down a terminal's xterm instance.
 *
 * `captureScreen` defaults to true — the buffer is serialized into the
 * metadata store so the tab can repaint from it later. Pass `false` when the
 * TAB ITSELF is being destroyed: there is no future reader, and the write
 * would land on a row the retention sweep is about to reclaim (or already
 * has), stranding a full serialized scrollback in the store.
 */
export function disposeTerminalInstance(id: string, opts?: { captureScreen?: boolean }): void {
  const instance = instances.get(id)
  if (!instance)
    return
  const captureScreen = opts?.captureScreen ?? true
  // Serialize the live buffer BEFORE tearing the instance down.
  //
  // This is the right altitude for the capture: the bytes live in the xterm
  // instance, and this is the one place every path that destroys one passes
  // through. Hanging it off the workspace switch instead covered only that
  // caller — a terminal tab dragged to another workspace, or a floating window
  // closed with a terminal in it, unmounts its view and disposes here with no
  // switch involved, losing the scrollback silently. Nothing re-fetches it
  // either: `hydrated` is already true, so the tab comes back with whatever
  // bytes the FIRST hydration returned.
  //
  // `bufferHasVisibleContent` guards a real hazard: a freshly-mounted xterm
  // still parsing its snapshot through the write queue serializes BLANK, and
  // writing that back would erase the bytes `ListTerminals` returned.
  if (captureScreen && screenSink && bufferHasVisibleContent(instance.terminal)) {
    try {
      screenSink(id, serializeXtermBuffer(instance))
    }
    catch {
      // A serialization failure must never block teardown — the WebGL context
      // and the xterm instance below leak if we let it propagate.
    }
  }
  // Relinquish any pooled WebGL context first so the slot frees up for
  // another terminal. This is the single teardown chokepoint reached by every
  // path — explicit close, HMR dispose, and the unmount microtask below.
  webglPool.release(id)
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
  const getActiveInstance = () => (lastActiveTerminalId ? instances.get(lastActiveTerminalId) : undefined)

  ;(window as any).__getActiveTerminalText = () => {
    const instance = getActiveInstance()
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
  ;(window as any).__getActiveTerminalRows = () => getActiveInstance()?.terminal.rows ?? 0
  ;(window as any).__getActiveTerminalBufferType = () => getActiveInstance()?.terminal.buffer.active.type ?? 'normal'
  // E2E hook: drive input through the same sendInput callback xterm's
  // onData handler uses, so the test exercises the full handleTerminalInput
  // routing (READY → SendInput RPC, EXITED → RestartTerminal RPC) without
  // depending on Playwright's keyboard focusing xterm's hidden textarea.
  ;(window as any).__sendActiveTerminalInput = (text: string) => {
    const instance = getActiveInstance()
    if (!instance?.sendInput)
      return false
    instance.sendInput(new TextEncoder().encode(text))
    return true
  }
  // E2E hooks for the WebGL context pool: assert that the number of live
  // contexts stays bounded and that hidden tabs hold none.
  ;(window as any).__webglTerminalCount = () => webglPool.size()
  ;(window as any).__terminalRendererFor = (id: string) => (instances.get(id)?.webglAddon ? 'webgl' : 'dom')
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
  /**
   * Whether the enclosing tile is the layout-focused tile. The xterm
   * focus effect below gates on this so a terminal that becomes the
   * active tab on its tile as a SIDE EFFECT (e.g. the user dragged a
   * different tab away and MRU rotated the terminal to the top of
   * the source tile) doesn't steal keyboard focus from wherever the
   * user is actually working. Clicking the terminal's tab still moves
   * `focusedTileId` to its tile first, so the common case still ends
   * with the cursor blinking in xterm.
   */
  tileFocused: boolean
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
    const resizeObserver = createRafResizeObserver(() => {
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
    resizeObserver?.observe(ref)

    onCleanup(() => {
      resizeObserver?.disconnect()
    })
  })

  // Re-fit when this terminal becomes active+visible. Focus is gated
  // on `tileFocused` so an MRU-driven rotation (e.g. user just
  // dragged the agent off this tile and the terminal got promoted to
  // the active tab on the now-unfocused source tile) doesn't grab
  // the keyboard cursor away from where the user is looking.
  createEffect(() => {
    if (props.active && props.visible) {
      const instance = instances.get(props.terminalId)
      if (instance) {
        const shouldFocus = props.tileFocused
        requestAnimationFrame(() => {
          const prevCols = instance.terminal.cols
          const prevRows = instance.terminal.rows
          instance.fitAddon.fit()
          if (shouldFocus)
            instance.terminal.focus()
          if (instance.terminal.cols !== prevCols || instance.terminal.rows !== prevRows) {
            props.onResize(props.terminalId, instance.terminal.cols, instance.terminal.rows)
          }
        })
      }
    }
  })

  // Grant or relinquish a pooled WebGL context as this terminal moves on and
  // off screen. Only the visible terminal in a tile (`active && visible`)
  // competes for the bounded WebGL pool; hidden tabs and any overflow beyond
  // the pool's capacity render via xterm's DOM renderer. `focused` is passed
  // synchronously so the terminal the user is typing in always wins a slot,
  // even on an initial multi-tile render where mount order would otherwise
  // decide. Deliberately no `onCleanup(release)`: a cross-tile move unmounts
  // this container and mounts another for the same id, and the pool's
  // coalesced reconcile already keeps that transient safe — unmount-driven
  // release is owned solely by disposeTerminalInstance (guarded by the
  // element-`isConnected` check below), so a move never releases.
  //
  // `instances.get` is read non-reactively: the entry is created by `onMount`
  // above, which — being registered before this effect — always runs first, so
  // the instance is present on this effect's first (and every) run while the
  // container is mounted. The only removal is disposeTerminalInstance, which
  // unmounts the container (disposing this effect) in the same breath.
  createEffect(() => {
    const onScreen = props.active && props.visible
    const instance = instances.get(props.terminalId)
    if (onScreen && instance)
      webglPool.acquire(props.terminalId, instance, { focused: props.tileFocused })
    else
      webglPool.release(props.terminalId)
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
  // refreshTerminalFont re-arms each instance's `fontsReady` for the new
  // family (so a later or in-flight pool attach builds the atlas from the new
  // font) and, once the variants load, clears the atlas of any terminal that
  // currently holds a WebGL context (a no-op on DOM-rendered ones).
  //
  // Guard on a real family change: `monoFontFamily()` re-emits whenever the
  // preferences store hydrates (a fresh `monoFonts` array is a new signal
  // value even when the resolved string is identical), and re-fitting every
  // instance on each spurious re-fire is wasted layout/reflow work.
  //
  // `instances` is the module-level map shared by every mounted TerminalView,
  // so each tile's copy of this effect iterates all instances. Fit only the
  // instances refreshTerminalFont actually re-armed (it returns `false` once a
  // sibling view already applied the swap), so a font change costs M reflows,
  // not tiles x M.
  let lastFontFamily: string | undefined
  createEffect(() => {
    const family = preferences.monoFontFamily()
    if (family === lastFontFamily)
      return
    lastFontFamily = family
    for (const [, instance] of instances) {
      if (refreshTerminalFont(instance, family, DEFAULT_FONT_SIZE))
        instance.fitAddon.fit()
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
  //
  // Guard on a real theme change, mirroring the font effect above:
  // `resolveTerminalTheme` returns one of two module-level ITheme constants,
  // so a reference compare cheaply skips redundant re-applies when a
  // theme-adjacent signal fires without changing the resolved theme (e.g.
  // prefers-color-scheme flips while the terminal theme is pinned to
  // light/dark). Because `instances` is the module-level map shared by every
  // mounted TerminalView, an unguarded re-fire costs tiles x instances xterm
  // palette rebuilds — each `options.theme` write recomputes the color table.
  let lastTheme: ITheme | undefined
  createEffect(() => {
    const theme = resolveTerminalTheme(
      preferences.terminalTheme(),
      preferences.theme(),
      prefersDark(),
    )
    if (theme === lastTheme)
      return
    lastTheme = theme
    for (const [, instance] of instances) {
      // Skip instances already on this theme. `instances` is the shared
      // module-level map, so every mounted TerminalView (one per tile) runs
      // this effect; without the per-instance guard a theme flip costs
      // tiles x instances palette rebuilds instead of one per instance, since
      // each write recomputes xterm's color table. `options.theme` returns the
      // stored reference and `resolveTerminalTheme` yields stable constants, so
      // the compare is exact. Mirrors refreshTerminalFont's per-instance guard
      // in the font effect above.
      if (instance.terminal.options.theme !== theme)
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

  // The row list is keyed on TERMINAL IDS, not on the `TerminalTab` objects.
  //
  // A `TerminalTab` is a join result (see tabView), rebuilt whenever ANY field it
  // derives from `tabMetadata` changes -- an OSC title the shell emits on every
  // prompt, a status transition, a cols/rows write, the MRU stamp a click leaves.
  // `<For>` keys by item IDENTITY, so keying rows on the object made each of those
  // dispose the row and mount a fresh one: `TerminalContainer` unmounts, which
  // tears down the xterm instance, releases its pooled WebGL context, and
  // re-creates all of it from the serialized buffer. That round-trip is why it
  // LOOKED fine -- `disposeTerminalInstance` captures the scrollback on the way
  // out and the new instance repaints it -- but a shell that sets its title per
  // prompt rebuilt its terminal on every command.
  //
  // Ids are strings, so `shallowEqualArrays` means the `<For>` reconciles only
  // when a terminal is actually added, removed, or reordered. Every other field is
  // read reactively INSIDE the row, where a change belongs: Solid updates the
  // existing `TerminalContainer`'s props in place instead of replacing it.
  const { keys: terminalIds, byKey: terminalById } = createKeyedRows(() => props.terminals, t => t.id)

  return (
    <div class={styles.container} data-theme={terminalThemeMode()}>
      <div class={styles.terminalInner}>
        <For each={terminalIds()}>
          {(id) => {
            // A memo, not a bare thunk: `terminalById` is rebuilt whenever ANY
            // tab's metadata changes anywhere in the account, so a thunk would
            // re-evaluate all six derived props on every row for a write that
            // touched none of them. `TerminalTab` is identity-stable across
            // recomputes (tabView compares with `shallowEqual`), so the memo
            // settles on `===` and the propagation stops here.
            const terminal = createMemo(() => terminalById().get(id))
            return (
              <Switch
                fallback={(
                  <TerminalContainer
                    terminalId={id}
                    active={id === props.activeTerminalId}
                    visible={props.visible}
                    tileFocused={props.tileFocused}
                    screen={terminal()?.screen}
                    lastOffset={props.getLastOffset?.(id)}
                    cols={terminal()?.cols}
                    rows={terminal()?.rows}
                    fontFamily={preferences.monoFontFamily()}
                    fontSize={DEFAULT_FONT_SIZE}
                    theme={terminalTheme()}
                    contentReady={(terminal()?.contentReady ?? false)
                      || terminal()?.status === TerminalStatus.EXITED
                      || terminal()?.status === TerminalStatus.DISCONNECTED}
                    startupMessage={terminal()?.startupMessage}
                    onInput={props.onInput}
                    onResize={props.onResize}
                    onTitleChange={props.onTitleChange}
                    onBell={props.onBell}
                    onContentReady={props.onContentReady}
                  />
                )}
              >
                <Match when={terminal()?.status === TerminalStatus.STARTUP_FAILED}>
                  <div
                    class={styles.startupErrorPane}
                    classList={{ [styles.terminalWrapperHidden]: id !== props.activeTerminalId }}
                    data-testid="terminal-startup-error"
                  >
                    <StartupErrorBody
                      title="Terminal failed to start"
                      error={terminal()?.startupError ?? ''}
                    />
                  </div>
                </Match>
              </Switch>
            )
          }}
        </For>
      </div>
    </div>
  )
}
