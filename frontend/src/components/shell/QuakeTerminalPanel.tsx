import type { Component } from 'solid-js'
import type { UntrustedLinkConfirm } from '~/lib/untrustedLinks'
import type { QuakeKey, QuakeTerminalStore } from '~/stores/quakeTerminal.store'
import type { TerminalTab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { X } from 'lucide-solid'
import { createEffect, createMemo, createSignal, onCleanup, onMount, Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { TerminalView } from '~/components/terminal/TerminalView'
import { usePreferences } from '~/context/PreferencesContext'
import * as styles from './QuakeTerminalPanel.css'

export interface QuakeTerminalPanelProps {
  quakeStore: QuakeTerminalStore
  view: TabView
  metadata: TabMetadataStore
  /**
   * The quake key of the FOCUSED tab, or null when it has none.
   *
   * A key, not a tab id, because the panel belongs to a working directory: two
   * tabs in one directory show the same panel, and switching between them must
   * not swap the shell underneath. Null only when the focused tab has no worker
   * or no working directory -- every tab TYPE has a panel, agent or not.
   */
  activeQuakeKeyId: () => string | null
  /** Hide the panel of one directory. Wired to the store's `close`. */
  onClose: (key: QuakeKey) => void
  onInput: (terminalId: string, data: Uint8Array) => void
  onResize: (terminalId: string, cols: number, rows: number) => void
  onContentReady: (terminalId: string) => void
  /** Whether a tab rename is open anywhere; see TerminalViewProps.tabEditing. */
  tabEditing?: () => boolean
  /** See TerminalViewProps.confirmLink. A quake terminal's links raise the same dialog. */
  confirmLink: UntrustedLinkConfirm
}

/**
 * The quake terminal: a shell that slides over the centre area for one working
 * DIRECTORY.
 *
 * Nothing renders until the first open, which is what makes the feature lazy --
 * a user who never presses the shortcut pays no xterm, no RPC and no watch
 * entry. Once a panel exists it stays MOUNTED while closed, and only its
 * transform changes: unmounting would trip `TerminalView`'s cleanup dispose and
 * throw away the WebGL slot and the live buffer on every toggle, which is the
 * opposite of what a quake terminal is for.
 *
 * Every live quake terminal goes to one `TerminalView`, and only the focused
 * directory's is visible. That is what makes switching tabs instant: each
 * terminal keeps its own xterm and scrollback, and `TerminalView` hides the rest
 * with `visibility: hidden` so their dimensions stay valid. Only the visible one
 * competes for a WebGL context. Switching between two tabs of the SAME
 * directory changes nothing here at all -- they resolve to one key, so the same
 * shell stays on screen mid-keystroke.
 */
export const QuakeTerminalPanel: Component<QuakeTerminalPanelProps> = (props) => {
  const preferences = usePreferences()

  const entry = createMemo(() => {
    const keyId = props.activeQuakeKeyId()
    return keyId === null ? undefined : props.quakeStore.entryFor(keyId)
  })

  /**
   * Every quake terminal this client holds, as terminal tabs.
   *
   * Read from the view's own memo rather than mapped back through
   * `getTerminalTab` here: the view already assembles this exact list, and a
   * second reconstruction per render is one more place a field can be served
   * differently.
   */
  const terminals = (): TerminalTab[] => props.view.detachedTerminalTabs()

  /**
   * Whether the panel painted its closed state at least once.
   *
   * False for the render that CREATES the panel, and true from the next
   * microtask onward. See `armFirstSlide` for why the first open needs that.
   */
  const [firstSlideArmed, setFirstSlideArmed] = createSignal(false)

  const open = () => firstSlideArmed() && entry()?.open === true

  /**
   * Two jobs the panel element owns, both of which need the element itself.
   *
   * FIRST SLIDE. A CSS transition interpolates between two computed values,
   * and the panel does not exist until the first open of its lifetime -- so an
   * element inserted already carrying `data-quake-open="true"` has no earlier
   * value to leave, and it appears fully in place instead of sliding. Every LATER open animates on
   * its own, because the panel stays mounted once it exists. Reading a layout
   * property supplies the missing value: it forces the browser to compute style
   * and layout for the CLOSED panel before the open one lands in the same task.
   * A `requestAnimationFrame` would not, because its callback runs BEFORE the
   * paint of the frame that inserted the element.
   *
   * INERTNESS. The closed panel stays in the DOM, so it must leave the tab
   * order. Written as an ATTRIBUTE rather than through the `inert` prop, which
   * Solid sets as a DOM property: an engine without the property drops it
   * silently, and the closed panel then stays reachable by Tab -- a keyboard
   * user landing in a terminal that is off screen. The attribute is what every
   * engine reads, and it is also what a test can see.
   */
  const armFirstSlide = (el: HTMLDivElement) => {
    onMount(() => {
      void el.offsetHeight
      setFirstSlideArmed(true)
    })
    // Re-armed for the NEXT element. The panel unmounts once the last quake
    // terminal is released -- the last tab in its directory closed, or its shell
    // exited -- and the reopen after that builds a new element, which needs the
    // same two painted values the first one did.
    onCleanup(() => setFirstSlideArmed(false))
    createEffect(() => {
      if (open())
        el.removeAttribute('inert')
      else
        el.setAttribute('inert', '')
    })
  }

  // The panel exists as soon as ANY directory has one, not just the focused
  // one: a background directory's shell must keep receiving bytes, and its
  // xterm has to stay mounted for that.
  const anyPanel = () => props.quakeStore.detachedTerminals().length > 0 || entry() !== undefined

  return (
    <Show when={anyPanel()}>
      <div
        class={styles.quakeClip}
        style={{
          // One property drives ONE axis: the PANEL's orientation rule picks
          // whether it lands on height or width, so there is no second value to
          // keep in step. It rides on the clip because the clip is the box that
          // percentage resolves against -- the centre area -- and the panel
          // inherits it from here. The opacity is formatted as a percentage
          // because `color-mix` takes one directly.
          '--quake-size': `${preferences.quakeSizePercent()}%`,
          '--quake-duration': `${preferences.quakeAnimationMs()}ms`,
          '--quake-opacity': `${preferences.quakeBackgroundOpacity() * 100}%`,
        }}
      >
        <div
          ref={armFirstSlide}
          class={styles.quakePanel}
          data-testid="quake-panel"
          // The hook the shell's focus restore reads: a close only pulls the
          // caret back to the composer when focus is still inside this box.
          data-quake-panel
          data-quake-orientation={preferences.quakeOrientation()}
          data-quake-open={open() ? 'true' : 'false'}
          // A closed panel is still in the DOM, so it must be out of the
          // accessibility tree and out of the tab order -- otherwise a keyboard
          // user tabs into a terminal that is off screen. `inert` covers focus,
          // `aria-hidden` covers the reader.
          aria-hidden={open() ? undefined : 'true'}
        >
          {/* The one pointer route to hide the panel.
              A keyboard user has the toggle chord, but the panel covers the
              whole centre area and a touch device has no chord at all -- a
              panel the Control CLI opened on a phone was otherwise impossible
              to dismiss without a page reload. */}
          <Show when={entry()}>
            {shown => (
              <IconButton
                class={styles.quakeClose}
                icon={X}
                iconSize="xs"
                title="Hide the quake terminal"
                aria-label="Hide the quake terminal"
                onClick={() => props.onClose(shown())}
              />
            )}
          </Show>
          <div class={styles.quakeBody}>
            <TerminalView
              terminals={terminals()}
              activeTerminalId={entry()?.terminalId || null}
              visible={open()}
              // The panel takes focus while it is open, which is what makes
              // TerminalView focus the xterm on the way in.
              tileFocused={open()}
              tabEditing={props.tabEditing}
              confirmLink={props.confirmLink}
              getLastOffset={id => props.metadata.get(id)?.lastOffset}
              onInput={props.onInput}
              onResize={props.onResize}
              onContentReady={props.onContentReady}
              transparentBackground
            />
          </div>
        </div>
      </div>
    </Show>
  )
}
