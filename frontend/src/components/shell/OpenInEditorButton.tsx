import type { Component } from 'solid-js'
import type { DetectedEditor } from '~/api/platformBridge'
import Check from 'lucide-solid/icons/check'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createMemo, createResource, createSignal, For, getOwner, onMount, runWithOwner, Show } from 'solid-js'
import { getRuntimeState, platformBridge } from '~/api/platformBridge'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { EditorIcon } from '~/components/common/EditorIcons'
import { Tooltip } from '~/components/common/Tooltip'
import {
  getPreferredEditorId,
  loadDetectedEditors,
  setPreferredEditorId,
} from '~/lib/externalEditors'
import { createLogger } from '~/lib/logger'
import { shortcutHint } from '~/lib/shortcuts/display'
import { spinner } from '~/styles/animations.css'
import * as styles from './OpenInEditorButton.css'

const log = createLogger('open-in-editor')

// Hoisted: Intl.Collator construction is non-trivial; reusing one across
// every refresh keeps the sortedEditors memo cheap.
const editorCollator = new Intl.Collator(undefined, { sensitivity: 'base' })

interface OpenInEditorButtonProps {
  /** Active tab's working directory, reactively read. */
  workingDir: () => string | undefined
}

export const OpenInEditorButton: Component<OpenInEditorButtonProps> = (props) => {
  // Capture the reactive owner at setup time. The refresh handler runs from
  // a setTimeout callback (to clear the popover dismiss transition); without
  // re-establishing the owner, every refetchEditors() call there leaks
  // internal Solid computations, and those leaks accumulate across refreshes
  // until each refresh blocks the main thread long enough to clamp the chat
  // view's scrollTop to 0. runWithOwner restores the original parent so
  // those computations get disposed properly.
  const owner = getOwner()

  // Solo-Desktop gate: ask the Rust shell, not the URL. In `task dev-desktop`
  // the webview points at http://localhost:4328, so a URL-based check would
  // wrongly classify a solo run as non-solo. The runtime state's `localSolo`
  // capability flag reflects the actual sidecar shell mode.
  const [runtimeState] = createResource(() => getRuntimeState())
  const eligible = () => runtimeState()?.capabilities.localSolo ?? false

  const [editors, { refetch: refetchEditors }] = createResource<DetectedEditor[], boolean>(
    eligible,
    async canRun => (canRun ? loadDetectedEditors() : []),
    { initialValue: [] },
  )

  const [refreshing, setRefreshing] = createSignal(false)

  const [preferredId, setPreferredId] = createSignal<string | undefined>(undefined)
  onMount(() => {
    setPreferredId(getPreferredEditorId())
  })

  const [menuOpen, setMenuOpen] = createSignal(false)

  // Sort detected editors alphabetically by display name. Stable sort + a
  // collator so accented names file correctly. Memoized so the menu doesn't
  // re-sort on every render.
  const sortedEditors = createMemo<DetectedEditor[]>(() => {
    const list = editors().slice()
    list.sort((a, b) => editorCollator.compare(a.displayName, b.displayName))
    return list
  })

  // The MRU may have been an editor that's since been uninstalled — in that
  // case we hide the brand label and treat the button like "no MRU set".
  const preferredEditor = createMemo<DetectedEditor | undefined>(() => {
    const id = preferredId()
    if (!id)
      return undefined
    return editors().find(e => e.id === id)
  })

  const launch = (id: string) => {
    const dir = props.workingDir()
    if (!dir)
      return
    setPreferredId(id)
    setPreferredEditorId(id)
    platformBridge.openInEditor(id, dir).catch((err: unknown) => {
      log.warn('open_in_editor failed', { id, dir, err })
    })
  }

  const choose = (id: string) => {
    // Menu picks set the MRU and close the dropdown — they do NOT launch.
    // The user runs the editor by clicking the main face afterwards (or
    // pressing the keyboard shortcut).
    setPreferredId(id)
    setPreferredEditorId(id)
    setMenuOpen(false)
  }

  const handleMainClick = () => {
    const ed = preferredEditor()
    if (ed) {
      launch(ed.id)
    }
    else {
      // No usable MRU → open the menu so the user can pick.
      setMenuOpen(true)
    }
  }

  // Selector used by the post-refresh scroll-restore guard below to find
  // active chat scroll containers in the DOM.
  const CHAT_SCROLL_CONTAINER_SELECTOR = '[data-chat-scroll-container="true"]'
  // Skip the restore if the chat's scrollHeight changed by more than this
  // many pixels — that signals a real content reload, not the spurious
  // clamp we're trying to undo.
  const CHAT_SCROLL_RESTORE_HEIGHT_TOLERANCE_PX = 200

  const runRefresh = async () => {
    if (refreshing())
      return
    setRefreshing(true)
    try {
      const fresh = await loadDetectedEditors(true)
      // Only emit a new editors() signal if the set actually changed. The
      // common case (user hits refresh just to check) returns an identical
      // list — skipping the refetch avoids re-running the menu's <For>
      // and the layout pass that comes with it.
      const current = editors()
      const sameList = fresh.length === current.length
        && fresh.every((e, i) => e.id === current[i]?.id && e.displayName === current[i]?.displayName)
      if (!sameList) {
        // refetchEditors creates internal Solid computations. When called
        // from a setTimeout callback (well past any reactive tracking
        // context) without re-entering the component owner, those
        // computations leak. Re-entering ensures they get parented and
        // disposed correctly.
        if (owner)
          await runWithOwner(owner, () => refetchEditors())
        else
          await refetchEditors()
      }
      // If the MRU points at an editor that's no longer detected, fall
      // back to the first remaining one (mirrors the keyboard-shortcut
      // handler's behavior). Empty list → clear in-memory MRU but leave
      // localStorage alone, so the user's choice returns when they
      // reinstall the editor.
      const mru = preferredId()
      if (mru && !fresh.some(ed => ed.id === mru)) {
        if (fresh.length > 0) {
          setPreferredId(fresh[0].id)
          setPreferredEditorId(fresh[0].id)
        }
        else {
          setPreferredId(undefined)
        }
      }
    }
    catch (err) {
      log.warn('refresh editors failed', err)
    }
    finally {
      setRefreshing(false)
    }
  }

  const handleRefresh = () => {
    if (refreshing())
      return

    // When the editor list actually changes, the refetchEditors → Solid
    // flush → DOM diff → browser layout pass takes long enough (~70ms)
    // to block rAF, and during that gap the active chat tile's scrollTop
    // gets reset to 0 by some browser-internal mechanism that we
    // couldn't pinpoint despite extensive instrumentation. Snapshot
    // scrollTop on every chat container before the refresh and restore
    // it after the layout pass settles.
    //
    // Iterate over all chat containers (the visible tab plus any hidden
    // ones) so each preserves its own state — querySelector alone would
    // only catch the first DOM match, which is often the hidden tab.
    const snapshots = Array.from(
      document.querySelectorAll<HTMLDivElement>(CHAT_SCROLL_CONTAINER_SELECTOR),
    ).map(el => ({
      el,
      scrollTop: el.scrollTop,
      scrollHeight: el.scrollHeight,
    }))
    const restoreChatScroll = () => {
      for (const s of snapshots) {
        if (!s.el.isConnected)
          continue
        if (s.el.scrollTop === s.scrollTop)
          continue
        const heightDelta = Math.abs(s.el.scrollHeight - s.scrollHeight)
        if (heightDelta < CHAT_SCROLL_RESTORE_HEIGHT_TOLERANCE_PX)
          s.el.scrollTop = s.scrollTop
      }
    }

    void runRefresh().finally(() => {
      // Run after Solid's flush + the browser's layout pass have settled.
      requestAnimationFrame(() => requestAnimationFrame(restoreChatScroll))
    })
  }

  const mainTooltip = () => {
    const ed = preferredEditor()
    const label = ed ? `Open in ${ed.displayName}` : 'Open in external editor'
    return shortcutHint(label, 'app.openInExternalEditor')
  }

  // Stay rendered while refreshing so the chevron's spinner can run even if
  // the freshly-fetched editor list is momentarily empty.
  const visible = () => eligible() && !!props.workingDir() && (editors().length > 0 || refreshing())

  // Position the popover relative to the whole split-button container, not
  // the chevron alone. Anchoring to the chevron (which is small and lives
  // near the right edge of the title bar) makes the menu's auto-flip clamp
  // it leftward into a column that overlaps the "Open in …" face. Anchoring
  // to the container keeps the menu's left edge aligned with the button.
  let containerRef: HTMLDivElement | undefined

  return (
    <Show when={visible()}>
      <div
        ref={el => (containerRef = el)}
        class={styles.splitButton}
        data-testid="open-in-editor"
      >
        <Tooltip text={mainTooltip()} ariaLabel>
          <button
            type="button"
            class={styles.mainFace}
            onClick={handleMainClick}
            data-testid="open-in-editor-main"
          >
            <Show
              when={preferredEditor()}
              fallback={(
                <>
                  <EditorIcon size={14} />
                  <span>Open in …</span>
                </>
              )}
            >
              {ed => (
                <>
                  <EditorIcon id={ed().id} size={14} />
                  <span>
                    Open in
                    {' '}
                    {ed().displayName}
                  </span>
                </>
              )}
            </Show>
          </button>
        </Tooltip>
        <DropdownMenu
          class={styles.menu}
          data-testid="open-in-editor-menu"
          open={menuOpen}
          onToggle={setMenuOpen}
          anchorRef={() => containerRef}
          trigger={triggerProps => (
            <Tooltip text={refreshing() ? 'Refreshing editor list…' : 'Choose editor'} ariaLabel>
              <button
                type="button"
                // Intentionally NOT calling triggerProps.ref — that would
                // make the chevron the positioning anchor. We want the whole
                // splitButton container (passed via anchorRef above) to be
                // the anchor, so the menu's left edge aligns with the
                // button's left edge instead of being clamped leftward by
                // auto-flip near the viewport's right edge.
                onPointerDown={triggerProps.onPointerDown}
                onClick={triggerProps.onClick}
                aria-expanded={triggerProps['aria-expanded']}
                aria-haspopup="menu"
                class={styles.chevronFace}
                disabled={refreshing()}
                data-testid="open-in-editor-chevron"
              >
                <Show when={refreshing()} fallback={<ChevronDown size={12} />}>
                  <RefreshCw size={12} class={spinner} />
                </Show>
              </button>
            </Tooltip>
          )}
        >
          <For each={sortedEditors()}>
            {editor => (
              <button
                type="button"
                role="menuitem"
                class={`${styles.menuItem}${editor.id === preferredEditor()?.id ? ` ${styles.menuItemSelected}` : ''}`}
                onClick={() => choose(editor.id)}
                data-testid={`open-in-editor-item-${editor.id}`}
              >
                <span class={styles.menuItemValue}>
                  <EditorIcon id={editor.id} size={16} />
                  <span>{editor.displayName}</span>
                </span>
                <Show when={editor.id === preferredEditor()?.id}>
                  <Check size={14} class={styles.check} />
                </Show>
              </button>
            )}
          </For>
          <hr class={styles.menuSeparator} />
          <button
            type="button"
            role="menuitem"
            class={styles.menuItem}
            onClick={handleRefresh}
            disabled={refreshing()}
            data-testid="open-in-editor-refresh"
          >
            <span class={styles.menuItemValue}>
              <RefreshCw size={14} />
              <span>Refresh editor list</span>
            </span>
          </button>
        </DropdownMenu>
      </div>
    </Show>
  )
}
