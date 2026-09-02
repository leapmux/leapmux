import type { Component, JSX } from 'solid-js'
import type { GitFileStatusEntry } from '~/generated/proto/leapmux/v1/common_pb'
import type { DiffStats } from '~/stores/repoGit'
import { Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { GitFileStatusCode } from '~/generated/proto/leapmux/v1/common_pb'
import * as dtStyles from './DirectoryTree.css'
import * as gsStyles from './gitStatusUtils.css'
import { labelWithStats } from './sharedTree.css'

/** Return a CSS class and test ID to color the file icon based on git status. */
export function getGitFileIconClass(entry: GitFileStatusEntry): { class: string, testId: string | undefined } {
  if (entry.unstagedStatus === GitFileStatusCode.UNMERGED || entry.stagedStatus === GitFileStatusCode.UNMERGED)
    return { class: dtStyles.iconConflict, testId: 'git-status-unstaged' }
  if (entry.unstagedStatus === GitFileStatusCode.UNTRACKED)
    return { class: dtStyles.iconUntracked, testId: 'git-status-untracked' }
  if (entry.stagedStatus !== GitFileStatusCode.UNSPECIFIED && entry.unstagedStatus === GitFileStatusCode.UNSPECIFIED)
    return { class: dtStyles.iconStaged, testId: 'git-status-staged' }
  return { class: dtStyles.iconUnstaged, testId: 'git-status-unstaged' }
}

/** Diff stats badge showing +N -M *U. */
export const DiffStatsBadge: Component<{ stats: DiffStats, class?: string }> = (props) => {
  const added = () => props.stats.added
  const deleted = () => props.stats.deleted
  const untracked = () => props.stats.untracked
  const hasContent = () => added() > 0 || deleted() > 0 || untracked() > 0
  return (
    <Show when={hasContent()}>
      <span class={props.class ?? gsStyles.diffStats} data-testid="git-diff-stats">
        <Show when={added() > 0}>
          <span class={gsStyles.diffStatsAdded}>
            +
            {added()}
          </span>
        </Show>
        {added() > 0 && deleted() > 0 ? ' ' : ''}
        <Show when={deleted() > 0}>
          <span class={gsStyles.diffStatsDeleted}>
            -
            {deleted()}
          </span>
        </Show>
        {(added() > 0 || deleted() > 0) && untracked() > 0 ? ' ' : ''}
        <Show when={untracked() > 0}>
          <span class={gsStyles.diffStatsUntracked}>
            *
            {untracked()}
          </span>
        </Show>
      </span>
    </Show>
  )
}

/**
 * Tooltip content pattern used by repo/branch group headers and workspace
 * items: a plain-text label followed by the diff-stats badge. The badge
 * self-hides when all counts are zero or stats are missing.
 */
export const LabelWithDiffStats: Component<{ label: string, stats: DiffStats | null | undefined }> = props => (
  <span>
    {props.label}
    <Show when={props.stats}>
      {s => <DiffStatsBadge stats={s()} />}
    </Show>
  </span>
)

/**
 * Visible row pattern: a label + diff-stats badge wrapped as a single
 * Tooltip target. By default the tooltip appears only when the row is clipped
 * and reuses {@link LabelWithDiffStats} for content. `tooltipContent` replaces
 * that body and `showWhen` decides when it opens; the two are independent.
 *
 * `label` may be plain text or JSX (e.g. a styled inner span). `tooltipLabel`
 * is the text shown in the tooltip body — defaults to `label` when it's a
 * string; pass explicitly when the visible and tooltip texts differ
 * (e.g. short repo label vs full origin URL).
 */
export const RowLabelWithStats: Component<{
  label: JSX.Element
  tooltipLabel?: string
  /**
   * Replaces the default label+stats tooltip body.
   *
   * Pass no interactive element and no nested `Tooltip`: a tooltip's portal is
   * `pointer-events: none`, so nothing inside it can be reached.
   */
  tooltipContent?: JSX.Element
  /**
   * When the tooltip opens. Defaults to `'clipped'`, which repeats a label the
   * row had to truncate.
   *
   * A caller that passes `tooltipContent` normally wants `'always'`, because a
   * custom body states facts that are nowhere else on the row — the sidebar's
   * branch row names the kind of checkout and its directory, and a
   * clipped-only tooltip would hide both whenever the label happens to fit.
   *
   * It is a PROP and never derived from `tooltipContent`. JSX passed as a prop
   * compiles to a lazy getter, so a `props.tooltipContent ? … : …` test builds
   * the whole body just to read its truthiness — and `Tooltip` reads `showWhen`
   * from a bare `setTimeout`, outside any owner, where those computations are
   * never disposed.
   */
  showWhen?: 'always' | 'clipped'
  stats: DiffStats | null | undefined
}> = (props) => {
  const tooltipText = () => props.tooltipLabel ?? (typeof props.label === 'string' ? props.label : '')
  return (
    <Tooltip
      content={props.tooltipContent ?? <LabelWithDiffStats label={tooltipText()} stats={props.stats} />}
      showWhen={props.showWhen ?? 'clipped'}
    >
      <span class={labelWithStats}>
        {props.label}
        <Show when={props.stats}>
          {s => <DiffStatsBadge stats={s()} />}
        </Show>
      </span>
    </Tooltip>
  )
}
