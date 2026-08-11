import type { JSX } from 'solid-js'
import type { ProviderSettingChange, ProviderSettingsAction } from '~/components/chat/providers/registry'
import type { AgentProvider, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { EnterKeyMode } from '~/lib/browserStorage'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import GitBranch from 'lucide-solid/icons/git-branch'
import Paperclip from 'lucide-solid/icons/paperclip'
import Plus from 'lucide-solid/icons/plus'
import { createMemo, For, Show } from 'solid-js'
import { pluginFor } from '~/components/chat/providers/registry'
import { hasOptions, resolvedCurrent } from '~/components/chat/settingsGroups'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { BranchContextMenu } from '~/components/workspace/BranchContextMenu'
import { DEFAULT_DISABLED_PLACEHOLDER } from '~/lib/editor/keyboardPlugins'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import { formatShortcut } from '~/lib/shortcuts/display'
import * as styles from './composer.css'
import { OptionGroupPopover } from './OptionGroupPopover'

/**
 * Props for the composer's `[+]` menu.
 */
export interface ComposerPlusMenuProps {
  /** The full option-group catalog. */
  optionGroups: AvailableOptionGroup[] | undefined
  /** Optimistic option-value map keyed by group id. */
  optionValues: Record<string, string>
  /** Provider, used to resolve the mode axis label and declared actions. */
  agentProvider?: AgentProvider
  /** Dispatch a settings change (any axis). Optional to match the panel's `onChange?`. */
  onSettingChange?: (change: ProviderSettingChange) => void
  /** Open the OS file picker. Disabled during a control request. */
  onAttachFile: () => void
  /** Whether attaching files is currently allowed (false during control requests). */
  canAttach: boolean
  /**
   * Whether the composer accepts input at all (false for a non-steerable
   * subagent). Such a composer can neither send nor apply a settings change,
   * so the settings submenus, the provider actions, and attach all honour it.
   * The view toggles below stay live: they are local preferences.
   */
  disabled?: boolean
  /**
   * Why the composer accepts no input, shown on the disabled attach item.
   *
   * Pass the SAME string the panel gives the editor as its disabled
   * placeholder, so the box, the hint above it, and this menu state one reason
   * and cannot disagree. Both surfaces apply the same fallback to the same
   * input, so an absent reason still resolves identically.
   */
  disabledReason?: string
  /**
   * Whether a settings change is in flight. A model or effort change relaunches
   * the session and takes seconds, and every settings surface flips its label
   * optimistically the moment a value is picked — so without a busy marker the
   * user cannot tell an applied change from a pending one, and picks again.
   *
   * The marker lives on this menu's trigger, not on the status bar: the bar is a
   * preference this menu can switch off, which would otherwise take the only
   * feedback with it.
   */
  settingsLoading?: boolean
  /** Resolved Enter-key mode. */
  enterKeyMode: () => EnterKeyMode
  /** Toggle Enter-key mode. */
  onToggleEnterMode: () => void
  /**
   * The current git branch, or undefined when the agent reports none. The
   * branch submenu renders only when it is set, matching the status-bar chip.
   */
  branchName?: string
  /** Open the "Change branch..." dialog. */
  onChangeBranch?: () => void
  /** Open the "Delete branch..." dialog. */
  onDeleteBranch?: () => void
  /** Why both branch actions are unusable (e.g. the Worker is offline). */
  branchDisabledReason?: string
  /** Whether the status bar is shown. */
  showStatusBar: () => boolean
  /** Toggle status-bar visibility. */
  onToggleStatusBar: () => void
  /**
   * Renders the agent-info rows (context usage, rate limits, session). Omit to
   * hide the item. The status bar shows the same rows behind its own trigger,
   * but the bar can be switched off — and unlike every settings axis, this
   * information has no other home, so the menu carries it too.
   *
   * A FUNCTION, not a rendered element, for the same reason as
   * `ComposerStatusBar.infoTrigger`: Solid turns a JSX prop VALUE into a getter,
   * so an element built inside one is rebuilt whenever the getter's
   * dependencies change. Here that discarded and recreated every row of an open
   * card — resetting each copy button's "copied" check and dropping any text
   * the user had selected in it — on every status push.
   */
  agentInfo?: () => JSX.Element
}

/** Groups sorted by backend order, dropping empty groups. */
function sortedGroups(groups: AvailableOptionGroup[] | undefined): AvailableOptionGroup[] {
  return [...(groups ?? [])]
    .filter(hasOptions)
    .sort((a, b) => a.order - b.order)
}

/**
 * The composer's `[+]` menu: attach file, settings (one submenu per option
 * group + provider actions), send-mode toggle, and status-bar toggle. This is
 * the single comprehensive settings surface — the status-bar chips are
 * quick-access shortcuts to the same groups via the same `onSettingChange`.
 */
export function ComposerPlusMenu(props: ComposerPlusMenuProps): JSX.Element {
  // Only the ids drive the submenu list, so a catalog re-broadcast that leaves
  // the set of groups unchanged produces an identical array and `For` keeps
  // every submenu's DOM. Each submenu resolves its own group from the catalog.
  const groupIds = createMemo(() => sortedGroups(props.optionGroups).map(g => g.id), undefined, {
    equals: shallowEqualArrays,
  })

  // Provider-declared action buttons (e.g. Codex "Bypass permissions").
  const actions = createMemo<ProviderSettingsAction[]>(() => pluginFor(props.agentProvider)?.settingsActions ?? [])

  const attachDisabledReason = () => {
    if (props.disabled)
      return props.disabledReason || DEFAULT_DISABLED_PLACEHOLDER
    return props.canAttach ? undefined : 'Attach is unavailable during a control request'
  }

  return (
    <DropdownMenu
      trigger={triggerProps => (
        <Tooltip text={props.settingsLoading ? 'Applying a settings change…' : 'Add, settings, and more'} ariaLabel>
          <button
            class={styles.plusButton}
            data-testid="composer-plus-trigger"
            {...triggerProps}
          >
            {/* The in-flight marker rides the `[+]` button because this button
                is the one settings surface that is ALWAYS present: the status
                bar is a preference this very menu can switch off. The button
                stays clickable while it spins. */}
            <Show when={props.settingsLoading} fallback={<Icon icon={Plus} size="sm" />}>
              <Spinner size="xs" data-testid="settings-loading-spinner" />
            </Show>
          </button>
        </Tooltip>
      )}
      class={styles.plusPopover}
      data-testid="composer-plus-popover"
    >
      <button
        role="menuitem"
        data-testid="composer-attach-file"
        disabled={!props.canAttach || props.disabled}
        title={attachDisabledReason()}
        onClick={() => props.onAttachFile()}
      >
        <Icon icon={Paperclip} size="xs" />
        Attach file…
      </button>

      <hr />

      {/* Keyed by group id, not by the group object: the worker re-broadcasts
          the whole catalog on every status push, so the group the user is
          changing arrives as a fresh object. Keying by object would dispose
          and recreate that submenu — the one whose popover is open — mid-click. */}
      <For each={groupIds()}>
        {id => (
          <PlusGroupSubmenu
            groupId={id}
            optionGroups={props.optionGroups}
            optionValues={props.optionValues}
            onChange={props.onSettingChange}
            disabled={props.disabled}
          />
        )}
      </For>

      {/* Divide the two kinds of item: above are the axes that configure what
          the agent DOES, below is the session it is working in — its branch and
          its usage. Guarded on BOTH sides, so an agent that reports no option
          groups, or none of the session items, cannot leave this rule stranded
          against the one above or below it. */}
      <Show when={groupIds().length > 0 && (props.branchName || props.agentInfo)}>
        <hr />
      </Show>

      {/* The branch axis, for the same reason every option group is here: the
          status bar that normally hosts it is a preference this menu can switch
          off, and the only other route is the sidebar — which is itself hidden
          behind a toggle on a narrow layout. Same menu items, same guard. */}
      <Show when={props.branchName}>
        {branch => (
          <BranchContextMenu
            onChangeBranch={props.onChangeBranch ?? (() => {})}
            onDeleteBranch={props.onDeleteBranch ?? (() => {})}
            disabledReason={props.branchDisabledReason}
            data-testid="composer-plus-branch-popover"
            trigger={triggerProps => (
              // The trigger stays enabled — the two items inside it are what the
              // Worker guard disables — so the reason needs a real tooltip here.
              <Tooltip text={props.branchDisabledReason}>
                <button
                  role="menuitem"
                  class={styles.subTrigger}
                  data-testid="composer-plus-branch"
                  {...triggerProps}
                >
                  <span class={styles.subTriggerLabel}>
                    <Icon icon={GitBranch} size="xs" />
                    {branch()}
                  </span>
                  <Icon icon={ChevronRight} size="xs" />
                </button>
              </Tooltip>
            )}
          />
        )}
      </Show>

      <Show when={props.agentInfo}>
        {rows => (
          <DropdownMenu
            trigger={triggerProps => (
              <button
                role="menuitem"
                class={styles.subTrigger}
                data-testid="composer-agent-info"
                {...triggerProps}
              >
                Agent info
                <Icon icon={ChevronRight} size="xs" />
              </button>
            )}
            class={styles.infoPopover}
            data-testid="composer-agent-info-popover"
          >
            {rows()()}
          </DropdownMenu>
        )}
      </Show>

      <Show when={actions().length > 0}>
        <hr />
        <For each={actions()}>
          {action => (
            <button
              role="menuitem"
              data-testid={action.testId}
              disabled={props.disabled || Object.entries(action.sets).every(
                ([k, v]) => resolvedCurrent(props.optionGroups, props.optionValues, k) === v,
              )}
              onClick={() => props.onSettingChange?.({ sets: { ...action.sets } })}
            >
              {action.label}
            </button>
          )}
        </For>
      </Show>

      <hr />

      <DropdownMenuCheckableItem
        kind="checkbox"
        label={`Send with ${formatShortcut('$mod+Enter')}`}
        checked={props.enterKeyMode() === 'cmd-enter-sends'}
        onSelect={() => props.onToggleEnterMode()}
      />

      <DropdownMenuCheckableItem
        kind="checkbox"
        label="Show status bar"
        checked={props.showStatusBar()}
        onSelect={() => props.onToggleStatusBar()}
      />
    </DropdownMenu>
  )
}

/**
 * One option-group submenu inside the `[+]` ▸ settings area, rendered with the
 * nested-DropdownMenu submenu pattern (the same one `WorkspaceContextMenu`'s
 * "Move to" uses).
 */
function PlusGroupSubmenu(props: {
  groupId: string
  optionGroups: AvailableOptionGroup[] | undefined
  optionValues: Record<string, string>
  onChange?: (change: ProviderSettingChange) => void
  disabled?: boolean
}): JSX.Element {
  return (
    <OptionGroupPopover
      groupId={props.groupId}
      optionGroups={props.optionGroups}
      optionValues={props.optionValues}
      onChange={props.onChange}
      disabled={props.disabled}
      popoverClass={styles.subPopover}
      // The status-bar chip renders the SAME group with the same per-option
      // test ids, so a locator for an option matches twice. A name on this popover
      // lets a caller say which surface it means.
      popoverTestId={`composer-group-${props.groupId}-popover`}
      trigger={(triggerProps, view) => (
        <button
          role="menuitem"
          class={styles.subTrigger}
          data-testid={`composer-group-${props.groupId}`}
          {...triggerProps}
        >
          {view.label}
          <Icon icon={ChevronRight} size="xs" />
        </button>
      )}
    />
  )
}
