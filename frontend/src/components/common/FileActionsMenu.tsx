import type { Component } from 'solid-js'
import type { FileSaveActions } from '~/components/common/fileSaveActions'
import type { PathFlavor } from '~/lib/paths'
import type { PopoverPositionOptions } from '~/lib/popoverPosition'
import AtSign from 'lucide-solid/icons/at-sign'
import Copy from 'lucide-solid/icons/copy'
import Download from 'lucide-solid/icons/download'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import TerminalIcon from 'lucide-solid/icons/terminal'
import { createSignal, getOwner, runWithOwner, Show } from 'solid-js'
import { isTauriApp } from '~/api/platformBridge'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { createFileSaveActions } from '~/components/common/fileSaveActions'
import { Icon } from '~/components/common/Icon'
import { IconButton } from '~/components/common/IconButton'
import { copyTextToClipboard } from '~/lib/clipboard'
import { relativizePath } from '~/lib/paths'

/**
 * Shared file/dir actions context menu. Hosts Mention / Open terminal /
 * Copy path / Copy relative path / Save as / Save to Downloads /
 * Download as a single three-dot menu, used by both `DirectoryTree` and
 * the file viewer surfaces.
 *
 * Items are gated by the callbacks / props that are actually provided:
 *   - `onMention`     → "Mention in chat"
 *   - `isDir && onOpenTerminal` → "Open a terminal tab here"
 *   - always           → "Copy path"
 *   - `rootPath`       → "Copy relative path"
 *   - `!isDir`         → "Download" (web) or "Save as..." + "Save to Downloads" (desktop)
 */
export interface FileActionsMenuProps {
  workerId: string
  path: string
  flavor: PathFlavor
  isDir?: boolean
  rootPath?: string
  homeDir?: string
  /** Optional handler for the "Mention in chat" item. */
  onMention?: (path: string) => void
  /** Optional handler for "Open terminal" — only shown for directories. */
  onOpenTerminal?: (dirPath: string) => void
  /** CSS class applied to the trigger IconButton. */
  triggerClass?: string
  /** data-testid for the trigger IconButton (default: 'file-actions-trigger'). */
  triggerTestId?: string
  /**
   * Prefix used for menu-item `data-testid` attributes. Default
   * `file-actions`; callers can override (e.g. DirectoryTree uses
   * `tree`) so tests can assert on stable IDs without coupling to
   * this component's internals.
   *
   * Items produced: `{prefix}-mention-button`,
   * `{prefix}-open-terminal-button`, `{prefix}-copy-path-button`,
   * `{prefix}-copy-relative-path-button`, `{prefix}-download-button`
   * (web), `{prefix}-save-as-button` + `{prefix}-save-to-downloads-button`
   * (desktop).
   */
  itemTestIdPrefix?: string
  /** Positioning options passed through to DropdownMenu. */
  placement?: PopoverPositionOptions
  /**
   * Shared save/download actions. When omitted, the menu creates its
   * own instance. Pass an external one to share busy state with another
   * surface (e.g. FileViewer's unsupported-card buttons) so clicking a
   * save in one place disables it in the other.
   */
  actions?: FileSaveActions
}

export const FileActionsMenu: Component<FileActionsMenuProps> = (props) => {
  const tid = (suffix: string) => `${props.itemTestIdPrefix ?? 'file-actions'}-${suffix}`

  // Lazy ownership of FileSaveActions: allocate on first open of the
  // dropdown so DirectoryTree rows (thousands of them, the vast majority
  // never opened) don't pay the per-row signal + context cost up front.
  // Skipped entirely when the caller injects `props.actions` (FileViewer
  // shares its own instance with the unsupported-card buttons) or for
  // directory rows where the save items don't render.
  //
  // `usePreferences()` inside `createFileSaveActions` needs the component
  // owner; click/toggle handlers run as plain DOM events without one, so
  // we capture the owner here and re-enter it on first use.
  const componentOwner = getOwner()
  const [menuOpen, setMenuOpen] = createSignal(false)
  let ownedActions: FileSaveActions | null = null
  const ensureOwnedActions = (): FileSaveActions => {
    if (!ownedActions) {
      ownedActions = runWithOwner(componentOwner, () => createFileSaveActions({
        workerId: () => props.workerId,
        path: () => props.path,
        flavor: () => props.flavor,
      }))!
    }
    return ownedActions
  }
  const actions = (): FileSaveActions => props.actions ?? ensureOwnedActions()
  // Reading `actions().op()` here triggers `ensureOwnedActions`.
  // Gate it on `menuOpen()` so the disabled-binding refire at mount
  // (popover hidden) doesn't allocate for rows the user never opens.
  // Externally-injected actions skip the gate — their busy state is
  // shared with another surface and must stay live even when this
  // menu's popover is closed.
  const busy = () => {
    if (props.actions)
      return props.actions.op() !== null
    return menuOpen() && actions().op() !== null
  }

  return (
    <DropdownMenu
      placement={props.placement}
      onToggle={setMenuOpen}
      trigger={triggerProps => (
        <IconButton
          icon={MoreHorizontal}
          iconSize="sm"
          size="sm"
          class={props.triggerClass}
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            triggerProps.onClick()
          }}
          ref={triggerProps.ref}
          onPointerDown={(e: PointerEvent) => {
            e.stopPropagation()
            triggerProps.onPointerDown()
          }}
          aria-expanded={triggerProps['aria-expanded']}
          data-testid={props.triggerTestId ?? 'file-actions-trigger'}
        />
      )}
    >
      <Show when={props.onMention}>
        <button
          role="menuitem"
          data-testid={tid('mention-button')}
          onClick={() => props.onMention?.(props.path)}
        >
          <Icon icon={AtSign} size="sm" />
          Mention in chat
        </button>
      </Show>
      <Show when={props.isDir && props.onOpenTerminal}>
        <button
          role="menuitem"
          data-testid={tid('open-terminal-button')}
          onClick={() => props.onOpenTerminal?.(props.path)}
        >
          <Icon icon={TerminalIcon} size="sm" />
          Open a terminal tab here
        </button>
      </Show>
      <button
        role="menuitem"
        data-testid={tid('copy-path-button')}
        onClick={() => copyTextToClipboard(props.path)}
      >
        <Icon icon={Copy} size="sm" />
        Copy path
      </button>
      <Show when={props.rootPath !== undefined}>
        <button
          role="menuitem"
          data-testid={tid('copy-relative-path-button')}
          onClick={() => {
            const rel = relativizePath(props.path, props.rootPath!, props.homeDir, props.flavor)
            copyTextToClipboard(rel)
          }}
        >
          <Icon icon={Copy} size="sm" />
          Copy relative path
        </button>
      </Show>
      <Show when={!props.isDir}>
        <Show
          when={isTauriApp()}
          fallback={(
            <button
              role="menuitem"
              data-testid={tid('download-button')}
              disabled={busy()}
              onClick={() => actions().handleDownload()}
            >
              <Icon icon={Download} size="sm" />
              Download
            </button>
          )}
        >
          <button
            role="menuitem"
            data-testid={tid('save-as-button')}
            disabled={busy()}
            onClick={() => actions().handleSaveAs()}
          >
            <Icon icon={Download} size="sm" />
            Save as...
          </button>
          <button
            role="menuitem"
            data-testid={tid('save-to-downloads-button')}
            disabled={busy()}
            onClick={() => actions().handleSaveToDownloads()}
          >
            <Icon icon={Download} size="sm" />
            Save to Downloads
          </button>
        </Show>
      </Show>
    </DropdownMenu>
  )
}
