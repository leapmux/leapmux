import type { JSX } from 'solid-js'
import type { ProviderSettingChange } from '~/components/chat/providers/registry'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { Show } from 'solid-js'
import { pluginFor } from '~/components/chat/providers/registry'
import { groupHasOptions, OPTION_ID_EFFORT, OPTION_ID_MODEL } from '~/components/chat/settingsGroups'
import { tabGitBranchLabel } from '~/stores/tab.helpers'
import * as styles from './composer.css'
import { GitBranchChip } from './GitBranchChip'
import { OptionAxisChip } from './OptionAxisChip'

/**
 * Props for the composer status bar.
 */
export interface ComposerStatusBarProps {
  /** The agent (carries option groups, git status, provider). */
  agent?: AgentInfo
  /** Optimistic option-value map keyed by group id. */
  optionValues: Record<string, string>
  /** Dispatch a settings change for the model/effort/mode chips. Optional to match the panel's `onChange?`. */
  onSettingChange?: (change: ProviderSettingChange) => void
  /** Raw flat `gitBranch` from the tab; resolved via {@link tabGitBranchLabel}. */
  branchName?: string
  /** Branch chip callbacks. */
  onChangeBranch: () => void
  onDeleteBranch: () => void
  /** Why the branch actions are unusable (e.g. worker offline). */
  branchDisabledReason?: string
  /**
   * Why the composer accepts no input at all, when it does not (a non-steerable
   * subagent). The chips dispatch real settings RPCs, so they must honour it —
   * and its PRESENCE is what disables them, so a dead chip always states the
   * same reason the rest of the composer states.
   */
  disabledReason?: string
  /**
   * Renders the right-cluster info trigger (ContextUsage + RateLimit popover).
   * Omit when the agent has nothing to show.
   *
   * A FUNCTION, not a rendered element. Solid turns a JSX prop VALUE into a
   * getter, so an element built inside one is discarded and rebuilt whenever the
   * getter's dependencies change — and the panel's `agent` prop takes a new
   * identity on every tab update, several times per streaming turn. The insert
   * effect would then swap the live node, disposing the popover the user is
   * reading mid-stream. A stable function reference cannot churn: `Show`'s
   * truthiness memo below absorbs everything except an actual appear/disappear.
   */
  infoTrigger?: () => JSX.Element
}

/** A group is present and offers at least one option. */
function hasGroup(agent: AgentInfo | undefined, id: string): boolean {
  return groupHasOptions(agent?.optionGroups, id)
}

/**
 * The slim status bar beneath the composer box: left cluster = GitBranch chip
 * + Model/Effort/Mode chips; right cluster = the session info trigger
 * (ContextUsage + RateLimit). Each chip is hidden when its underlying group is
 * absent (pre-handshake model, a provider without effort, etc.), mirroring the
 * fused trigger label's `hasGroup` test.
 */
export function ComposerStatusBar(props: ComposerStatusBarProps): JSX.Element {
  // The provider-declared mode axis (permissionMode for Claude, collaboration_mode
  // for Codex, primaryAgent for OpenCode/Kilo, …). Reused verbatim from the old
  // fused trigger label so the chip shows the same "mode" the panel does.
  const modeGroupKey = () => pluginFor(props.agent?.agentProvider)?.triggerModeGroupKey

  return (
    <div class={styles.statusBar} data-testid="composer-status-bar">
      <div class={styles.statusBarLeft}>
        <GitBranchChip
          branchName={tabGitBranchLabel(props.branchName, props.agent?.gitStatus?.branch)}
          disabledReason={props.branchDisabledReason}
          onChangeBranch={props.onChangeBranch}
          onDeleteBranch={props.onDeleteBranch}
        />
        <Show when={hasGroup(props.agent, OPTION_ID_MODEL)}>
          <OptionAxisChip
            groupId={OPTION_ID_MODEL}
            optionGroups={props.agent?.optionGroups}
            optionValues={props.optionValues}
            onChange={props.onSettingChange}
            disabledReason={props.disabledReason}
            testIdPrefix="composer-model"
          />
        </Show>
        <Show when={hasGroup(props.agent, OPTION_ID_EFFORT)}>
          <OptionAxisChip
            groupId={OPTION_ID_EFFORT}
            optionGroups={props.agent?.optionGroups}
            optionValues={props.optionValues}
            onChange={props.onSettingChange}
            disabledReason={props.disabledReason}
            optional
            testIdPrefix="composer-effort"
          />
        </Show>
        <Show when={modeGroupKey() && hasGroup(props.agent, modeGroupKey()!)}>
          <OptionAxisChip
            groupId={modeGroupKey()!}
            optionGroups={props.agent?.optionGroups}
            optionValues={props.optionValues}
            onChange={props.onSettingChange}
            disabledReason={props.disabledReason}
            optional
            testIdPrefix="composer-mode"
          />
        </Show>
      </div>
      <div class={styles.statusBarRight}>
        <Show when={props.infoTrigger}>
          {render => render()()}
        </Show>
      </div>
    </div>
  )
}
