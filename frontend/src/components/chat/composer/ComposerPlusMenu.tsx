import type { JSX } from 'solid-js'
import type { ProviderSettingChange, ProviderSettingsAction } from '~/components/chat/providers/registry'
import type { AgentProvider, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { EnterKeyMode } from '~/lib/browserStorage'
import type { DiffStats } from '~/stores/repoGit'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import Paperclip from 'lucide-solid/icons/paperclip'
import Plus from 'lucide-solid/icons/plus'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { pluginFor } from '~/components/chat/providers/registry'
import { hasOptions, resolvedCurrent } from '~/components/chat/settingsGroups'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { WorkingTreeIcon, WorkingTreeRows } from '~/components/common/WorkingTree'
import { BranchContextMenu } from '~/components/workspace/BranchContextMenu'
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
  /**
   * Why the composer accepts no input, when it does not. Its PRESENCE is what
   * disables the attach item and every settings submenu, so a dead item with no
   * stated reason is unrepresentable.
   *
   * It is the SAME string the panel gives the editor as its disabled
   * placeholder, so the box and this menu state one reason and cannot disagree.
   */
  disabledReason?: string
  /**
   * Whether a settings change is in flight. A model or effort change relaunches
   * the session and takes seconds, and every settings surface flips its label
   * optimistically the moment the user picks a value — so without a busy marker the
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
  /** True iff the agent's checkout is a linked worktree ({@link repoGitView}). */
  isWorktree?: boolean
  /** Absolute working-tree root ({@link repoGitView}'s `toplevel`). */
  directory?: string
  /** The agent's worker home directory, for the submenu tooltip's tilde path. */
  homeDir?: string
  /** Diff stats for the submenu tooltip's badge. */
  branchStats?: DiffStats | null
  /** Open the "Change branch..." dialog. */
  onChangeBranch?: () => void
  /** Open the "Delete branch..." / "Delete worktree..." dialog. */
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
   * the user selected in it — on every status push.
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
 * Everything that decides WHICH ROWS EXIST between the attach item and the two
 * view toggles. See the `structure` memo, which holds one of these still.
 */
interface MenuStructure {
  groupIds: string[]
  actions: ProviderSettingsAction[]
  branchName?: string
  agentInfo?: () => JSX.Element
}

/** Whether `s` draws anything BETWEEN the attach item and the view toggles. */
function hasMenuRows(s: MenuStructure): boolean {
  return s.groupIds.length > 0 || !!s.branchName || !!s.agentInfo || s.actions.length > 0
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
  const liveGroupIds = createMemo(() => sortedGroups(props.optionGroups).map(g => g.id), undefined, {
    equals: shallowEqualArrays,
  })

  // Provider-declared action buttons (e.g. Codex "Bypass permissions").
  const liveActions = createMemo<ProviderSettingsAction[]>(() => pluginFor(props.agentProvider)?.settingsActions ?? [])

  const [open, setOpen] = createSignal(false)

  /**
   * Everything that decides WHICH ROWS EXIST, held still while the menu is open.
   *
   * The menu is drawn from live props, and a status push supplies groups, a
   * branch, agent info and provider actions -- each of which inserts rows ABOVE
   * the two toggles at the bottom. A pointer already aimed at "Send with Enter"
   * then lands on whatever slid into its place, and one of those is a provider
   * action that applies a setting the moment the user clicks it.
   *
   * Reading `open()` FIRST and returning `prev` without touching any live source
   * is what freezes it: that run subscribes to `open` alone, so no push can
   * re-run this memo until the menu closes. `hasMenuRows(prev)` reads the HELD
   * snapshot, a plain object, so the condition below adds no subscription.
   *
   * ONE snapshot rather than a memo per field, because the fields are not
   * independent -- `hasMiddleSection` fences the region they share, so a mix of
   * fresh and stale values could draw a rule around nothing.
   *
   * `hasMenuRows(prev)`, not `prev !== undefined`: an EMPTY middle section is
   * nothing to hold still, and a freeze on one STRANDS the menu. Every settings
   * axis, the branch and the agent info arrive together on the first push, so a
   * menu opened before that push holds the attach item and the two toggles
   * alone, and it never refills: the user must close it and open it again, with
   * nothing on screen to say so, and this menu is the only settings surface once
   * the status bar is off. The option list in `./OptionGroupPopover` already
   * applies the same rule one level down.
   *
   * The trade: while the middle section is empty, its rows can still appear
   * under the pointer. That is the one case the freeze cannot cover without the
   * stranding, and the first push ends it.
   *
   * Only the STRUCTURE is frozen. Labels, checked state, the disabled reason and
   * the spinner keep reading live, because staleness THERE would be its own bug.
   */
  const structure = createMemo<MenuStructure>((prev) => {
    if (open() && prev !== undefined && hasMenuRows(prev))
      return prev
    return {
      groupIds: liveGroupIds(),
      actions: liveActions(),
      branchName: props.branchName,
      agentInfo: props.agentInfo,
    }
  })

  const groupIds = () => structure().groupIds
  const actions = () => structure().actions
  const branchName = () => structure().branchName
  const agentInfo = () => structure().agentInfo
  // One spelling of the default for the three places that render the kind: the
  // menu that names the delete item, the tooltip rows and the trigger's glyph.
  // A tab whose git status has not landed reports nothing, and "branch" is the
  // safe reading -- it claims nothing about a directory that a delete removes.
  const isWorktree = () => props.isWorktree ?? false

  // Whether anything renders BETWEEN the attach item and the view toggles. Both
  // rules that fence that region are drawn only when it is non-empty: a fresh
  // tab before its first status push has no groups, no branch, no agent info and
  // no provider actions, and two unconditional rules then landed side by side.
  //
  // Derived from the HELD snapshot through the same predicate the freeze reads,
  // so the rules, the rows they fence, and the decision to hold them still
  // cannot disagree about what is drawn.
  const hasMiddleSection = () => hasMenuRows(structure())

  const attachDisabledReason = () => {
    if (props.disabledReason)
      return props.disabledReason
    return props.canAttach ? undefined : 'Attach is unavailable during a control request'
  }

  return (
    <DropdownMenu
      // Drives the structure freeze above: the rows may not change shape under
      // a pointer that is already aimed at one of them.
      onToggle={setOpen}
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
      <Tooltip text={attachDisabledReason()}>
        <button
          role="menuitem"
          data-testid="composer-attach-file"
          disabled={!props.canAttach || !!props.disabledReason}
          onClick={() => props.onAttachFile()}
        >
          <Icon icon={Paperclip} size="xs" />
          Attach file…
        </button>
      </Tooltip>

      <Show when={hasMiddleSection()}><hr /></Show>

      {/* Keyed by group id, not by the group object: the worker re-broadcasts
          the whole catalog on every status push, so the group that the user
          changes arrives as a fresh object. Keying by object would dispose
          and recreate that submenu — the one whose popover is open — mid-click. */}
      <For each={groupIds()}>
        {id => (
          <PlusGroupSubmenu
            groupId={id}
            optionGroups={props.optionGroups}
            optionValues={props.optionValues}
            onChange={props.onSettingChange}
            disabledReason={props.disabledReason}
          />
        )}
      </For>

      {/* Divide the two kinds of item: above are the axes that configure what
          the agent DOES, below is the session that it works in — its branch and
          its usage. Guarded on BOTH sides, so an agent that reports no option
          groups, or none of the session items, cannot leave this rule stranded
          against the one above or below it. */}
      <Show when={groupIds().length > 0 && (branchName() || agentInfo())}>
        <hr />
      </Show>

      {/* The branch axis, for the same reason every option group is here: the
          status bar that normally hosts it is a preference this menu can switch
          off, and the only other route is the sidebar — which is itself hidden
          behind a toggle on a narrow layout. Same menu items, same guard. */}
      <Show when={branchName()}>
        {branch => (
          <BranchContextMenu
            isWorktree={isWorktree()}
            onChangeBranch={props.onChangeBranch ?? (() => {})}
            onDeleteBranch={props.onDeleteBranch ?? (() => {})}
            disabledReason={props.branchDisabledReason}
            data-testid="composer-plus-branch-popover"
            trigger={triggerProps => (
              // The trigger stays enabled — the two items inside it are what the
              // Worker guard disables — so the reason needs a real tooltip here.
              // Absent a reason it carries what the status-bar chip carries: the
              // kind of checkout and its directory. This menu exists because the
              // status bar is a preference the user can switch off, so it has to
              // state everything the bar states.
              <Tooltip
                text={props.branchDisabledReason}
                content={props.branchDisabledReason
                  ? undefined
                  : (
                      <WorkingTreeRows
                        isWorktree={isWorktree()}
                        name={branch()}
                        directory={props.directory ?? ''}
                        homeDir={props.homeDir}
                        stats={props.branchStats}
                      />
                    )}
              >
                <button
                  role="menuitem"
                  class={styles.subTrigger}
                  data-testid="composer-plus-branch"
                  {...triggerProps}
                >
                  <span class={styles.subTriggerLabel}>
                    <WorkingTreeIcon isWorktree={isWorktree()} size="xs" />
                    {branch()}
                  </span>
                  <Icon icon={ChevronRight} size="xs" />
                </button>
              </Tooltip>
            )}
          />
        )}
      </Show>

      <Show when={agentInfo()}>
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
            // A card of labelled rows, not a list of items: `card` keeps a
            // click on a row (or a drag across it, to select the text) from
            // closing the card, and carries the same surface the status bar's
            // copy of this card uses.
            as="card"
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
              disabled={!!props.disabledReason || Object.entries(action.sets).every(
                ([k, v]) => resolvedCurrent(props.optionGroups, props.optionValues, k) === v,
              )}
              onClick={() => props.onSettingChange?.({ sets: { ...action.sets } })}
            >
              {action.label}
            </button>
          )}
        </For>
      </Show>

      <Show when={hasMiddleSection()}><hr /></Show>

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
  /** Why the composer accepts no changes; its presence disables the options. */
  disabledReason?: string
}): JSX.Element {
  return (
    <OptionGroupPopover
      groupId={props.groupId}
      optionGroups={props.optionGroups}
      optionValues={props.optionValues}
      onChange={props.onChange}
      disabledReason={props.disabledReason}
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
