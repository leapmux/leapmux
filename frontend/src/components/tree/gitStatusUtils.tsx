import type { Component } from 'solid-js'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { DiffStats } from '~/stores/gitFileStatus.store'
import { Show } from 'solid-js'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import * as dtStyles from './DirectoryTree.css'
import * as gsStyles from './gitStatusUtils.css'

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
