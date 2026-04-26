import type { Component } from 'solid-js'
import Bot from 'lucide-solid/icons/bot'
import Terminal from 'lucide-solid/icons/terminal'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import * as styles from './AppShell.css'

interface EmptyTilePlaceholderProps {
  /** True when the active workspace is archived; an explanatory placeholder is shown instead of action buttons. */
  archived: boolean
  /**
   * True when this tile should show interactive action buttons (single-tile or focused multi-tile).
   * False shows a small "no tabs" hint instead — used for unfocused empty tiles in a multi-tile layout.
   */
  showActions: boolean
  onOpenAgent: () => void
  onOpenTerminal: () => void
}

export const EmptyTilePlaceholder: Component<EmptyTilePlaceholderProps> = (props) => {
  return (
    <Show
      when={!props.archived}
      fallback={(
        <div class={styles.placeholder} data-testid="tile-empty-state">
          This workspace is archived. Unarchive it to create new agents or terminals.
        </div>
      )}
    >
      <Show
        when={props.showActions}
        fallback={(
          <div class={styles.emptyTileHint} data-testid="empty-tile-hint">
            No tabs in this tile.
          </div>
        )}
      >
        <div class={styles.emptyTileActions} data-testid="empty-tile-actions">
          <button
            class="outline"
            data-testid="empty-tile-open-agent"
            onClick={() => props.onOpenAgent()}
          >
            <Icon icon={Bot} size="sm" />
            <span class={styles.emptyTileActionContent}>
              <span>Open a new agent tab...</span>
              <Show when={getShortcutHintsText('app.newAgent')}>
                {shortcut => <span class={styles.emptyTileActionShortcut}>{shortcut()}</span>}
              </Show>
            </span>
          </button>
          <button
            class="outline"
            data-testid="empty-tile-open-terminal"
            onClick={() => props.onOpenTerminal()}
          >
            <Icon icon={Terminal} size="sm" />
            <span class={styles.emptyTileActionContent}>
              <span>Open a new terminal tab...</span>
              <Show when={getShortcutHintsText('app.newTerminal')}>
                {shortcut => <span class={styles.emptyTileActionShortcut}>{shortcut()}</span>}
              </Show>
            </span>
          </button>
        </div>
      </Show>
    </Show>
  )
}
