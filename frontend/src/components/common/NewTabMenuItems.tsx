import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { For, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { Tooltip } from '~/components/common/Tooltip'
import { getShortcutHintsText, shortcutHint } from '~/lib/shortcuts/display'
import { menuSectionHeader } from '~/styles/shared.css'
import * as styles from './NewTabMenuItems.css'

export interface NewTabMenuItemsProps {
  /**
   * Every agent provider the TARGET worker reports. The icon row is absent
   * while this is empty or undefined -- an empty row would take a gap slot in
   * the menu and say nothing.
   */
  availableProviders?: AgentProvider[]
  /** Every shell the target worker reports, one menu item each. */
  availableShells?: string[]
  /** Which of `availableShells` the worker starts by default, if any. */
  defaultShell?: string
  /** Open an agent with this provider now, without a dialog. */
  onNewAgent: (provider: AgentProvider) => void
  /** Open the New agent dialog, pre-filled for the target. */
  onNewAgentAdvanced: () => void
  /** Open a terminal with this shell now, without a dialog. */
  onNewTerminalWithShell: (shell: string) => void
  /** Open the New terminal dialog, pre-filled for the target. */
  onNewTerminalAdvanced: () => void
  /**
   * Show the app-level keyboard-shortcut hints on the two dialog items.
   *
   * True only where the items act on the CURRENT tab context, which is what
   * those shortcuts do. A branch row's menu acts on that branch instead, so a
   * hint there would name a key that opens a different dialog.
   */
  shortcuts?: boolean
  /**
   * Why every item here is unusable, or undefined when they are usable.
   *
   * ONE reason for the whole block, because every item needs the same thing:
   * the Worker the agent or the terminal would run on. The reason goes through
   * `<Tooltip>`, which works on a disabled control and leaves each item its own
   * accessible name -- a `title` this long BECOMES the name instead.
   */
  disabledReason?: string
}

/**
 * The `Agents` and `Terminals` sections of a new-tab menu.
 *
 * Two menus render this: the tab bar's `+` / overflow menu, which opens a tab
 * at the current tab's working directory, and the branch context menu, which
 * opens one at that branch's checkout. The two used to be the tab bar's private
 * closure, so a branch menu that copied it would have been a second copy to
 * keep in step.
 *
 * The component is presentational. It neither fetches the two lists nor decides
 * where the new tab lands: the caller resolves both for the worker it means.
 */
export const NewTabMenuItems: Component<NewTabMenuItemsProps> = (props) => {
  const disabled = () => Boolean(props.disabledReason)

  return (
    <>
      <li class={menuSectionHeader}>Agents</li>
      <Show when={props.availableProviders?.length}>
        <li class={styles.providerIconsRow}>
          <For each={props.availableProviders}>
            {provider => (
              <Tooltip
                text={props.disabledReason ?? (
                  props.shortcuts
                    ? shortcutHint(`New ${agentProviderLabel(provider)} agent`, 'app.newAgent')
                    : `New ${agentProviderLabel(provider)} agent`
                )}
              >
                <button
                  type="button"
                  class={styles.providerButton}
                  data-testid={`menu-new-agent-${provider}`}
                  disabled={disabled()}
                  onClick={() => props.onNewAgent(provider)}
                >
                  <AgentProviderIcon provider={provider} size={16} />
                </button>
              </Tooltip>
            )}
          </For>
        </li>
      </Show>
      <Tooltip text={props.disabledReason}>
        <button
          role="menuitem"
          disabled={disabled()}
          onClick={() => props.onNewAgentAdvanced()}
        >
          <DropdownMenuItemContent
            label="New agent..."
            shortcut={props.shortcuts ? getShortcutHintsText('app.newAgentDialog') : undefined}
          />
        </button>
      </Tooltip>
      <hr />
      <li class={menuSectionHeader}>Terminals</li>
      <Tooltip text={props.disabledReason}>
        <button
          role="menuitem"
          disabled={disabled()}
          onClick={() => props.onNewTerminalAdvanced()}
        >
          <DropdownMenuItemContent
            label="New terminal..."
            shortcut={props.shortcuts ? getShortcutHintsText('app.newTerminalDialog') : undefined}
          />
        </button>
      </Tooltip>
      <For each={props.availableShells ?? []}>
        {shell => (
          <Tooltip text={props.disabledReason}>
            <button
              role="menuitem"
              disabled={disabled()}
              onClick={() => props.onNewTerminalWithShell(shell)}
            >
              <code>{shell}</code>
              <Show when={shell === props.defaultShell}>
                <span class={styles.shellDefault}>(default)</span>
              </Show>
            </button>
          </Tooltip>
        )}
      </For>
    </>
  )
}
