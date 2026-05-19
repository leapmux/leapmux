import type { Component } from 'solid-js'
import type { BranchGitState } from '~/generated/leapmux/v1/git_pb'
import { Show } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { pluralize } from '~/lib/plural'
import { diffStatsFromTabFields } from '~/stores/gitFileStatus.store'
import * as css from './BranchStatusInfo.css'

export interface BranchSnapshot {
  /** Worktree disposition — true when the branch is a linked worktree. */
  isWorktree: boolean
  worktreePath: string
  branchName: string
  /**
   * Shared diff/push snapshot. Undefined when the caller's RPC took a
   * fast path that skipped the snapshot — the component renders the
   * branch label without any uncommitted/unpushed lines in that case.
   */
  gitState?: BranchGitState
}

export interface AffectedTabs {
  agents: number
  terminals: number
  /**
   * File-tab count. FILE tabs hold no running process, so they never
   * contribute to the "will be stopped / keep running" verb — they're
   * listed alongside agents/terminals only when relevant (e.g. the
   * delete-branch dialog enumerating the whole branch group, or the
   * last-tab dialog where the closed tab is itself a FILE tab).
   */
  files: number
  /** True when these tabs will be stopped; false when they keep running. */
  willStop: boolean
}

export interface BranchStatusInfoProps {
  branch: BranchSnapshot
  affectedTabs: AffectedTabs
}

// True when nothing needs pushing — no uncommitted changes, no unpushed
// commits, a tracked upstream that still exists on the remote. Note: not
// gated by `canPush`; a branch with `canPush=false` but no local work is
// still "clean" by this definition.
function isClean(gs: BranchGitState): boolean {
  return !gs.hasUncommittedChanges
    && gs.unpushedCommitCount === 0
    && !gs.remoteBranchMissing
    && gs.upstreamExists
}

// True when there's actual push work to do. `canPush` is a capability
// check (origin exists, valid branch name), so dialogs that gate the
// Push button on `canPush` alone keep rendering it for clean trees;
// combine with this helper to hide the no-op.
export function hasPushableWork(gs: BranchGitState | undefined): boolean {
  if (!gs || !gs.canPush)
    return false
  return !isClean(gs)
}

// Shared status block rendered by both the last-tab-close dialog and the
// Delete branch dialog so they show identical fields and copy. The lines
// are stacked in an inner flex column with a small gap (see
// `BranchStatusInfo.css.ts`) so the consumer's own vstack gap can stay
// at its normal `gap-4` cadence without doubling against Oat's default
// `<p>` margin.
export const BranchStatusInfo: Component<BranchStatusInfoProps> = (props) => {
  return (
    <div class={css.statusLines}>
      <Show when={props.branch.isWorktree}>
        <div>
          Worktree:
          {' '}
          <code>{props.branch.worktreePath}</code>
        </div>
      </Show>
      <div>
        Branch:
        {' '}
        <code>{props.branch.branchName}</code>
      </div>
      <Show when={props.branch.gitState}>
        {gs => (
          <>
            <Show when={gs().hasUncommittedChanges}>
              <div>
                Uncommitted changes:
                {' '}
                <DiffStatsBadge stats={diffStatsFromTabFields(gs())} />
              </div>
            </Show>
            <Show when={gs().unpushedCommitCount > 0}>
              <div>
                {pluralize(gs().unpushedCommitCount, 'commit')}
                {' '}
                not pushed.
              </div>
            </Show>
            <Show when={gs().remoteBranchMissing || (!gs().upstreamExists && gs().canPush)}>
              <div>Branch not pushed to remote.</div>
            </Show>
            <Show when={isClean(gs())}>
              <div>No uncommitted changes or unpushed commits.</div>
            </Show>
          </>
        )}
      </Show>
      <Show when={props.affectedTabs.agents > 0 || props.affectedTabs.terminals > 0 || props.affectedTabs.files > 0}>
        <div>{formatAffectedTabs(props.affectedTabs)}</div>
      </Show>
    </div>
  )
}

// "<agent(s)> and <terminal(s)> will be stopped." reads naturally when
// at least one process is involved. A FILE-only close has no process
// to stop, so the sentence collapses to "<n file(s)> will be closed."
// to avoid the misleading "will be stopped" verb.
function formatAffectedTabs(t: AffectedTabs): string {
  const processParts: string[] = []
  if (t.agents > 0)
    processParts.push(pluralize(t.agents, 'agent'))
  if (t.terminals > 0)
    processParts.push(pluralize(t.terminals, 'terminal'))
  if (processParts.length > 0) {
    const verb = t.willStop ? 'will be stopped' : 'will keep running'
    const head = `${processParts.join(' and ')} ${verb}`
    if (t.files > 0)
      return `${head}, ${pluralize(t.files, 'file')} will be closed.`
    return `${head}.`
  }
  return `${pluralize(t.files, 'file')} will be closed.`
}
