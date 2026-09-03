import type { JSX } from 'solid-js'
import type { ProviderPermissionPreset, ProviderSettingChangeHandler } from '~/components/chat/providerSettings'
import type { WorkingTreeInfo } from '~/components/common/WorkingTree'
import type { BranchMenuActions } from '~/components/workspace/branchActions'
import type { AgentProvider, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { EnterKeyMode } from '~/lib/browserStorage'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import Paperclip from 'lucide-solid/icons/paperclip'
import Plus from 'lucide-solid/icons/plus'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { pluginFor } from '~/components/chat/providers/registry'
import { permissionPresetAvailable } from '~/components/chat/providerSettings'
import { hasOptions, resolvedCurrent } from '~/components/chat/settingsGroups'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { WorkingTreeIcon, WorkingTreeTooltip } from '~/components/common/WorkingTree'
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
  /** Provider, used to resolve the mode axis label and permission presets. */
  agentProvider?: AgentProvider
  /** Dispatch a settings change (any axis). Optional to match the panel's `onChange?`. */
  onSettingChange?: ProviderSettingChangeHandler
  /** Open the OS file picker. Disabled during a control request. */
  onAttachFile: () => void
  /** Whether attaching files is currently allowed (false during control requests). */
  canAttach: boolean
  /**
   * Whether the composer accepts input at all (false for a non-steerable
   * subagent). Such a composer can neither send nor apply a settings change,
   * so the settings submenus, the permission actions, and attach all honour it.
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
   * The checkout the branch submenu names, resolved from {@link repoGitView}.
   * The submenu renders only when its `name` is set, matching the status-bar
   * chip.
   *
   * REQUIRED, and passed whole — the same value the status bar receives, from
   * the same accessor. See {@link ComposerStatusBarProps.workingTree} for why
   * neither surface applies a default of its own.
   */
  workingTree: WorkingTreeInfo
  /**
   * Every branch-submenu action, already bound to this agent's branch. Omit to
   * hide the submenu — its trigger opens a menu, so a trigger with no actions
   * would open one of dead items.
   */
  branchActions?: BranchMenuActions
  /** The Worker the branch is checked out on, for the submenu's own lists. */
  branchWorkerId?: string
  /** Why every branch action is unusable (e.g. the Worker is offline). */
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
  permissionActions: PermissionAction[]
  branchName?: string
  /**
   * Frozen WITH the branch name, because it names the destructive item rather
   * than merely labelling it. A live kind under a held name lets "Delete
   * branch..." become "Delete worktree..." beneath a pointer already aimed at
   * it, which is the same hazard as a row sliding into that place — the user
   * now clicks an action that removes a directory after reading one that does
   * not. The other fields of the checkout (its directory, its diff badge) stay
   * live: those are labels, and staleness there would be its own bug.
   */
  isWorktree: boolean
  /**
   * Frozen WITH the branch name for the same reason the kind is: the submenu
   * renders only when both are present, so a live bundle under a held name
   * could leave the row on screen with nothing behind it. The bundle's own
   * closures still read the branch at CLICK time, so freezing it here holds the
   * row still without acting on stale data.
   */
  branchActions?: BranchMenuActions
  agentInfo?: () => JSX.Element
}

/** Whether `s` draws anything BETWEEN the attach item and the view toggles. */
function hasMenuRows(s: MenuStructure): boolean {
  return s.groupIds.length > 0 || !!s.branchName || !!s.agentInfo || s.permissionActions.length > 0
}

type PermissionActionKind = 'smart' | 'bypass'

interface PermissionAction {
  kind: PermissionActionKind
  label: string
  testId: string
  preset: ProviderPermissionPreset
}

const PERMISSION_ACTIONS: ReadonlyArray<Omit<PermissionAction, 'preset'>> = [
  { kind: 'smart', label: 'Smart permissions', testId: 'composer-smart-permissions' },
  { kind: 'bypass', label: 'Bypass permissions', testId: 'composer-bypass-permissions' },
]

function permissionActionsFor(
  provider: AgentProvider | undefined,
  groups: AvailableOptionGroup[] | undefined,
): PermissionAction[] {
  const presets = pluginFor(provider)?.permissionPresets
  if (!presets)
    return []
  const actions: PermissionAction[] = []
  for (const action of PERMISSION_ACTIONS) {
    const preset = presets[action.kind]
    if (permissionPresetAvailable(preset, groups))
      actions.push({ ...action, preset })
  }
  return actions
}

/**
 * The composer's `[+]` menu: attach file, settings (one submenu per option
 * group + permission actions), send-mode toggle, and status-bar toggle. This is
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

  const livePermissionActions = createMemo(() => permissionActionsFor(props.agentProvider, props.optionGroups))

  const [open, setOpen] = createSignal(false)

  /**
   * Everything that decides WHICH ROWS EXIST, held still while the menu is open.
   *
   * The menu is drawn from live props, and a status push supplies groups, a
   * branch, agent info and permission actions -- each of which inserts rows ABOVE
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
    // The submenu needs BOTH a name to show and actions to run, so the two
    // travel as one fact: `branchName` is set only when the bundle is there,
    // and `hasMenuRows` below then fences the region correctly either way.
    const branchActions = props.branchActions
    return {
      groupIds: liveGroupIds(),
      permissionActions: livePermissionActions(),
      branchName: branchActions ? props.workingTree.name || undefined : undefined,
      isWorktree: props.workingTree.isWorktree,
      branchActions,
      agentInfo: props.agentInfo,
    }
  })

  const groupIds = () => structure().groupIds
  const permissionActions = () => structure().permissionActions
  const branchName = () => structure().branchName
  const agentInfo = () => structure().agentInfo
  /** The held facts of the branch submenu, or undefined when it draws none. */
  const heldBranch = () => {
    const { branchName: name, branchActions, isWorktree } = structure()
    return name && branchActions ? { name, actions: branchActions, isWorktree } : undefined
  }

  // Whether anything renders BETWEEN the attach item and the view toggles. Both
  // rules that fence that region are drawn only when it is non-empty: a fresh
  // tab before its first status push has no groups, no branch, no agent info and
  // no permission actions, and two unconditional rules then landed side by side.
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

  const permissionActionAvailable = (action: PermissionAction) => {
    const currentPreset = pluginFor(props.agentProvider)?.permissionPresets?.[action.kind]
    return currentPreset === action.preset && permissionPresetAvailable(currentPreset, props.optionGroups)
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
      <Show when={heldBranch()}>
        {branch => (
          <BranchContextMenu
            isWorktree={branch().isWorktree}
            workerId={props.branchWorkerId ?? ''}
            actions={branch().actions}
            disabledReason={props.branchDisabledReason}
            data-testid="composer-plus-branch-popover"
            trigger={triggerProps => (
              // The trigger stays enabled — the items inside it are what the
              // Worker guard disables — so the reason needs a real tooltip here.
              // Absent a reason it carries what the status-bar chip carries: the
              // kind of checkout and its directory. This menu exists because the
              // status bar is a preference the user can switch off, so it has to
              // state everything the bar states, through the same component.
              //
              // The HELD kind and the HELD name, so the rows agree with the
              // item below them while the menu is open; the directory and the
              // badge stay live.
              <WorkingTreeTooltip
                disabledReason={props.branchDisabledReason}
                info={{ ...props.workingTree, isWorktree: branch().isWorktree, name: branch().name }}
              >
                <button
                  role="menuitem"
                  class={styles.subTrigger}
                  data-testid="composer-plus-branch"
                  {...triggerProps}
                >
                  <span class={styles.subTriggerLabel}>
                    <WorkingTreeIcon isWorktree={branch().isWorktree} size="xs" />
                    {branch().name}
                  </span>
                  <Icon icon={ChevronRight} size="xs" />
                </button>
              </WorkingTreeTooltip>
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

      <Show when={permissionActions().length > 0}>
        <hr />
        <For each={permissionActions()}>
          {action => (
            <button
              role="menuitem"
              data-testid={action.testId}
              disabled={!!props.disabledReason || !permissionActionAvailable(action) || Object.entries(action.preset.sets).every(
                ([k, v]) => resolvedCurrent(props.optionGroups, props.optionValues, k) === v,
              )}
              onClick={() => props.onSettingChange?.({ sets: { ...action.preset.sets } })}
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
  onChange?: ProviderSettingChangeHandler
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
