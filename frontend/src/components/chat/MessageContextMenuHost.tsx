import type { ParentComponent } from 'solid-js'
import type { MessageAction } from './messageActions'
import type { ContextMenuPress } from '~/components/common/contextMenuGesture'
import type { MenuInfoRow } from '~/components/common/MenuInfoRows'
import type { PopoverAnchor } from '~/lib/popoverPosition'
import { createSignal, For, Show, useContext } from 'solid-js'
import { pressAnchorRect } from '~/components/common/contextMenuGesture'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { MenuInfoButton } from '~/components/common/MenuInfoRows'
import { createStableContext } from '~/lib/createStableContext'
import { dangerMenuItem } from '~/styles/shared.css'
import { RelativeTimeAgo } from './RelativeTime'

/** One request from a message row asking the shared menu to open at `press`. */
export interface MessageContextMenuRequest {
  press: ContextMenuPress
  actions: MessageAction[]
  /** RFC3339 send time, for the info block. Omitted when the message carries none. */
  createdAt?: string
}

export interface MessageContextMenuHost {
  open: (request: MessageContextMenuRequest) => void
}

const MessageContextMenuContext = createStableContext<MessageContextMenuHost | undefined>(
  'chat/MessageContextMenuHost',
  undefined,
)

/**
 * Read the host from context. Returns `undefined` when no provider ancestor
 * exists -- a unit-test render tree that mounts a `MessageBubble` on its own. The
 * row then simply has no context menu rather than failing to render.
 */
export function useMessageContextMenu(): MessageContextMenuHost | undefined {
  return useContext(MessageContextMenuContext)
}

/**
 * Provide ONE shared context menu for every message row beneath it.
 *
 * Rationale, the same one recorded on ~/components/shell/GridPopoverHost.tsx:
 * `DropdownMenu` renders its children into the `popover="auto"` element, hidden
 * rather than unmounted. The chat list keeps 30-70 `MessageBubble` instances
 * mounted at once (the overscan band, plus the hidden premeasure copies) and
 * churns them on every fling, so a menu per row would mean dozens of hidden menus
 * built and torn down continuously. Routing every row through this singleton means
 * the items exist exactly once.
 *
 * The menu anchors to a RECT, not to an element -- the press point, which
 * `calcPopoverPosition` accepts directly. No row needs a phantom anchor element,
 * and a tall row (which a chat message often is) does not drag the menu down to
 * its own bottom edge.
 */
export const MessageContextMenuHostProvider: ParentComponent = (props) => {
  const [request, setRequest] = createSignal<MessageContextMenuRequest | null>(null)

  const host: MessageContextMenuHost = {
    // Replacing the request while the menu is up re-anchors it in place:
    // DropdownMenu's open effect tracks the anchor, so swapping `request()`
    // under a still-true `open` repositions with no close/reopen pass. (A
    // pointer-driven open is usually preceded by the browser's own light
    // dismiss, which closes the previous menu first; the keyboard menu key is
    // the path that opens over an open menu.)
    open: (req) => {
      setRequest(req)
    },
  }

  const anchor = (): PopoverAnchor | undefined => {
    const req = request()
    return req ? pressAnchorRect(req.press) : undefined
  }

  const infoRows = (): MenuInfoRow[] => {
    const createdAt = request()?.createdAt
    return createdAt ? [{ label: 'Sent:', value: <RelativeTimeAgo timestamp={createdAt} /> }] : []
  }

  /**
   * The toolbar's order, reversed.
   *
   * The toolbar runs broadest to narrowest -- the whole envelope's raw JSON first,
   * the rendered content last -- which suits a strip the reader scans. A menu is
   * read top-down from the pointer, so the same order would put the raw-JSON
   * debugging copy first and Quote near the bottom. Reversing lands the narrowest,
   * most-used actions nearest the cursor.
   */
  const menuActions = (): MessageAction[] =>
    [...(request()?.actions ?? []).filter(a => !a.danger)].reverse()

  /**
   * The destructive actions, kept OUT of the reversal and pinned to the foot of
   * the menu behind a rule.
   *
   * Reversing would otherwise float Delete to the very top, directly under the
   * cursor the menu just opened at -- the one place a destructive item must never
   * be. Every other menu in the app puts its danger item last, after an `<hr>`.
   */
  const dangerActions = (): MessageAction[] => (request()?.actions ?? []).filter(a => a.danger)

  return (
    <MessageContextMenuContext.Provider value={host}>
      {props.children}
      <DropdownMenu
        open={() => request() !== null}
        anchorRef={anchor}
        data-testid="message-context-menu"
        aria-label="Message actions"
        onToggle={(open) => {
          if (!open)
            setRequest(null)
        }}
      >
        {/* The same info block the worker and file menus lead with, so a message's
            send time reads the same way as a file's modified time. It is also the
            only place the timestamp is reachable without a hover: the toolbar's
            copy is inside the strip that only appears on one. */}
        <MenuInfoButton
          rows={infoRows()}
          copyText={() => request()?.createdAt ?? ''}
          toastMessage="Timestamp copied to clipboard"
          data-testid="message-menu-info"
        />
        <Show when={infoRows().length > 0 && menuActions().length > 0}>
          <hr />
        </Show>
        <For each={menuActions()}>
          {action => (
            <button
              role="menuitem"
              data-testid={`message-menu-${action.id}`}
              onClick={() => action.run()}
            >
              {action.label}
            </button>
          )}
        </For>
        <Show when={dangerActions().length > 0 && menuActions().length > 0}>
          <hr />
        </Show>
        <For each={dangerActions()}>
          {action => (
            <button
              role="menuitem"
              class={dangerMenuItem}
              data-testid={`message-menu-${action.id}`}
              onClick={() => action.run()}
            >
              {action.label}
            </button>
          )}
        </For>
      </DropdownMenu>
    </MessageContextMenuContext.Provider>
  )
}
