import { createMemo, createSignal } from 'solid-js'
import { shallowEqual } from '~/lib/shallowEqual'

/**
 * Identifiers for the GitOptions modes. Numeric enum (not string union)
 * so the comparison sites compile down to integer equality and TS
 * narrows discriminator-style switches exhaustively across the variants
 * of {@link GitModeIntent}. Values are frontend-only (never serialized
 * over the wire), so reordering is safe within this file.
 */
export enum GitMode {
  Current,
  SwitchBranch,
  CreateBranch,
  CreateWorktree,
  UseWorktree,
}

/**
 * Tagged-union payload emitted by GitOptions when the user's selection
 * changes. Each variant only carries fields that mode actually consumes
 * so callers don't have to pass undefined sentinels for unrelated modes.
 *
 * Every variant is a flat object of primitive fields, so `shallowEqual`
 * works as the `equals` callback for a `createMemo<GitModeIntent>` —
 * variants with different `mode` values also have different key counts,
 * so the discriminator drop-through is handled by the key-count check.
 */
export type GitModeIntent
  = | { mode: GitMode.Current }
    | { mode: GitMode.SwitchBranch, checkoutBranch: string, checkoutBranchError: string | null }
    | { mode: GitMode.CreateBranch, createBranch: string, createBranchError: string | null, createBranchBase: string }
    | { mode: GitMode.CreateWorktree, worktreeBranch: string, worktreeBranchError: string | null, worktreeBaseBranch: string }
    | { mode: GitMode.UseWorktree, useWorktreePath: string }

/**
 * Modes accepted by the ChangeBranchDialog. Defined here rather than at
 * the dialog so the validation helper and the dialog can both reference
 * the same tuple, and so {@link isChangeBranchSubmitDisabled} can
 * exhaustively check membership without enumerating each mode inline.
 */
export const CHANGE_BRANCH_MODES = [
  GitMode.SwitchBranch,
  GitMode.CreateBranch,
  GitMode.CreateWorktree,
] as const
export type ChangeBranchMode = (typeof CHANGE_BRANCH_MODES)[number]
export function isChangeBranchMode(mode: GitMode): mode is ChangeBranchMode {
  return CHANGE_BRANCH_MODES.includes(mode as ChangeBranchMode)
}

export interface GitFields {
  createWorktree: boolean
  worktreeBranch: string
  worktreeBaseBranch: string
  checkoutBranch: string
  createBranch: string
  createBranchBase: string
  useWorktreePath: string
}

// All RPC git fields blank — extended by the per-mode projections below
// with that mode's contributions. Kept internal so consumers can't
// accidentally build a partial payload without going through a typed
// projection.
const EMPTY_GIT_FIELDS: GitFields = {
  createWorktree: false,
  worktreeBranch: '',
  worktreeBaseBranch: '',
  checkoutBranch: '',
  createBranch: '',
  createBranchBase: '',
  useWorktreePath: '',
}

/**
 * Per-mode projections from a narrowed {@link GitModeIntent} to the
 * openAgent / openTerminal RPC field set. Each helper takes the matching
 * variant directly (the caller already switched on `mode`) and fills in
 * just that mode's fields; every other field is blanked so a stale
 * value from a previously-selected mode can't leak onto the wire.
 *
 * Use these from a `switch (intent.mode)` block where TypeScript has
 * already narrowed the intent; use {@link GitModeState.toGitFields} from
 * call sites that submit across any active mode without switching.
 */
export function fieldsForCheckoutBranch(
  intent: Extract<GitModeIntent, { mode: GitMode.SwitchBranch }>,
): GitFields {
  return { ...EMPTY_GIT_FIELDS, checkoutBranch: intent.checkoutBranch }
}
export function fieldsForCreateBranch(
  intent: Extract<GitModeIntent, { mode: GitMode.CreateBranch }>,
): GitFields {
  return {
    ...EMPTY_GIT_FIELDS,
    createBranch: intent.createBranch,
    createBranchBase: intent.createBranchBase,
  }
}
export function fieldsForCreateWorktree(
  intent: Extract<GitModeIntent, { mode: GitMode.CreateWorktree }>,
): GitFields {
  return {
    ...EMPTY_GIT_FIELDS,
    createWorktree: true,
    worktreeBranch: intent.worktreeBranch,
    worktreeBaseBranch: intent.worktreeBaseBranch,
  }
}
export function fieldsForUseWorktree(
  intent: Extract<GitModeIntent, { mode: GitMode.UseWorktree }>,
): GitFields {
  return { ...EMPTY_GIT_FIELDS, useWorktreePath: intent.useWorktreePath }
}

/**
 * Reactive store for the active GitModeIntent. GitOptions emits intents
 * via `handleGitModeChange` (a thin setter wrapper), consumers read
 * `currentIntent()` to validate or `toGitFields()` to build an RPC
 * payload. The intent is the single source of truth — there are no
 * parallel per-mode signals to keep in lockstep.
 *
 * `initial` lets a dialog opening on a non-default mode (e.g.
 * ChangeBranchDialog defaults to `SwitchBranch`) seed the signal up
 * front so the radio paints correctly on first render — GitOptions
 * reads its mode from this signal, so without the seed the dialog
 * would briefly show `Current` before the first emit replaces it.
 */
export function useGitModeState(initial: GitModeIntent = { mode: GitMode.Current }) {
  // Structural dedup at the signal so direct `handleGitModeChange`
  // callers (tests, future imperative call sites) don't notify
  // downstream effects on no-op writes — GitOptions's own outgoing
  // memo only protects the effect path.
  const [currentIntent, setIntent] = createSignal<GitModeIntent>(
    initial,
    { equals: shallowEqual },
  )

  // Memoed so per-keystroke writes inside a single mode (e.g. typing in
  // the CreateBranch input updates `currentIntent` via shallowEqual but
  // leaves `.mode` unchanged) don't refire every `gitMode()`-dependent
  // <Show> / effect downstream.
  const gitMode = createMemo(() => currentIntent().mode)
  const handleGitModeChange = (next: GitModeIntent) => setIntent(next)

  // Project the active GitModeIntent down to the seven RPC fields shared
  // by openAgent / openTerminal / NewWorkspaceDialog's openAgent. The
  // per-mode helpers above handle the actual field selection; this
  // delegator is for callers that don't switch on mode and just want
  // "whatever's currently selected" as one payload.
  const toGitFields = (): GitFields => {
    const i = currentIntent()
    switch (i.mode) {
      case GitMode.Current:
        // Spread so callers can safely mutate the returned object —
        // every other arm goes through a fieldsForXxx helper that
        // already spreads EMPTY_GIT_FIELDS, so returning the shared
        // singleton here would be an inconsistent mutation footgun.
        return { ...EMPTY_GIT_FIELDS }
      case GitMode.SwitchBranch:
        return fieldsForCheckoutBranch(i)
      case GitMode.CreateBranch:
        return fieldsForCreateBranch(i)
      case GitMode.CreateWorktree:
        return fieldsForCreateWorktree(i)
      case GitMode.UseWorktree:
        return fieldsForUseWorktree(i)
    }
  }

  return {
    gitMode,
    handleGitModeChange,
    toGitFields,
    currentIntent,
  }
}

export type GitModeState = ReturnType<typeof useGitModeState>
