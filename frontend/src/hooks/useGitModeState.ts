import { createMemo, createSignal } from 'solid-js'
import { shallowEqual } from '~/lib/shallowEqual'

/**
 * Identifiers for the GitOptions modes. Numeric enum (not string union)
 * so the comparison sites compile down to integer equality and TS
 * narrows discriminator-style switches exhaustively across the variants
 * of {@link GitModeIntent}.
 *
 * The NUMBERS are frontend-only and never leave memory, so reordering the
 * members stays safe. Anything that outlives the page -- the remembered
 * per-repository mode in browser storage -- stores a {@link GitModeToken}
 * instead, which is what keeps that promise true.
 */
export enum GitMode {
  Current,
  SwitchBranch,
  CreateBranch,
  CreateWorktree,
  UseWorktree,
}

/**
 * The name each mode goes by, EVERYWHERE it is named.
 *
 * One table so the dialog's radio and the menu item that opens the dialog on
 * that radio cannot drift: the item the user picks names what they then see.
 * `GitOptions` and `BranchContextMenu` held nine literals between them before
 * this, and pairing a mode with another mode's label was a plain typo away.
 *
 * `Record<GitMode, string>` makes a sixth mode a compile error rather than an
 * item that renders `undefined`.
 *
 * It lives HERE rather than in `GitOptions.tsx` because `BranchContextMenu`
 * renders once per branch row of every workspace, and `GitOptions.tsx` pulls in
 * `random-word-slugs`, `workerRpc`, `BranchSelect`, `WorktreeSelect` and
 * `validate`. This module imports `solid-js` and `~/lib/shallowEqual`, and both
 * surfaces already import it -- so the table costs no new import edge.
 */
export const GIT_MODE_LABELS: Record<GitMode, string> = {
  [GitMode.Current]: 'Use current state',
  [GitMode.SwitchBranch]: 'Switch to branch',
  [GitMode.CreateBranch]: 'Create new branch',
  [GitMode.CreateWorktree]: 'Create new worktree',
  [GitMode.UseWorktree]: 'Use existing worktree',
}

/**
 * The serialized spelling of a mode, for anything that outlives the page.
 *
 * A token rather than the enum's number, because {@link GitMode}'s own doc
 * licenses reordering its members -- and it can only keep doing so while no
 * stored value depends on the numbering. A stale number would silently select
 * a different mode after a reorder; a stale token resolves to nothing and the
 * caller falls back.
 */
export type GitModeToken
  = | 'current'
    | 'switch-branch'
    | 'create-branch'
    | 'create-worktree'
    | 'use-worktree'

/**
 * A `Record`, so a sixth mode is a compile error rather than a mode that
 * silently cannot be remembered.
 */
const GIT_MODE_TOKENS: Record<GitMode, GitModeToken> = {
  [GitMode.Current]: 'current',
  [GitMode.SwitchBranch]: 'switch-branch',
  [GitMode.CreateBranch]: 'create-branch',
  [GitMode.CreateWorktree]: 'create-worktree',
  [GitMode.UseWorktree]: 'use-worktree',
}

/** The token to store for `mode`. */
export function gitModeToken(mode: GitMode): GitModeToken {
  return GIT_MODE_TOKENS[mode]
}

/**
 * The mode a stored token names, or undefined for anything else.
 *
 * `unknown` rather than `GitModeToken`, because the argument comes back off the
 * wire of browser storage: a previous build, a hand edit, or a truncated write
 * can put any JSON there.
 */
export function gitModeFromToken(stored: unknown): GitMode | undefined {
  if (typeof stored !== 'string')
    return undefined
  for (const [mode, token] of Object.entries(GIT_MODE_TOKENS)) {
    if (token === stored)
      return Number(mode) as GitMode
  }
  return undefined
}

/**
 * The seed intent for a dialog opened directly on `mode`.
 *
 * Every field is empty, because the values belong to GitOptions: it owns the
 * branch picker, the name input and the base-branch picker, and it emits a
 * complete intent for its active mode on its first flush. What this seed
 * carries is the MODE alone, which GitOptions reads once (untracked) to paint
 * the correct radio on the first render.
 *
 * A dialog that seeds nothing paints its default mode first and swaps a moment
 * later, so every menu item that opens the dialog on its own mode would flash
 * the same one before landing.
 */
export function initialIntentForMode(mode: GitMode): GitModeIntent {
  switch (mode) {
    case GitMode.Current:
      return { mode }
    case GitMode.SwitchBranch:
      return { mode, checkoutBranch: '', checkoutBranchError: null }
    case GitMode.CreateBranch:
      return { mode, createBranch: '', createBranchError: null, createBranchBase: '' }
    case GitMode.CreateWorktree:
      return { mode, worktreeBranch: '', worktreeBranchError: null, worktreeBaseBranch: '' }
    case GitMode.UseWorktree:
      return { mode, useWorktreePath: '' }
  }
}

/**
 * The same name as a MENU item that opens a dialog on that mode.
 *
 * The three ASCII dots are the whole difference from {@link GIT_MODE_LABELS},
 * and they belong to the menu: an item that opens a dialog says so, and a radio
 * inside that dialog does not.
 */
export function gitModeMenuLabel(mode: GitMode): string {
  return `${GIT_MODE_LABELS[mode]}...`
}

/**
 * Tagged-union payload emitted by GitOptions when the user's selection
 * changes. Each variant only carries fields that mode actually consumes
 * so callers don't have to pass undefined sentinels for unrelated modes.
 *
 * Every variant is a flat object of primitive fields, so `shallowEqual`
 * works as the `equals` callback for a `createMemo<GitModeIntent>` — `mode` is
 * itself one of the compared keys, so two variants with different modes differ
 * on that key whatever their shapes are.
 *
 * Do NOT restate that as a key-count argument. `CreateBranch` and
 * `CreateWorktree` carry four keys each, so a reader who believes the counts
 * discriminate would think dropping `mode` from a payload is safe. It is not.
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

/**
 * The seed intent for a ChangeBranchDialog opened directly on `mode`.
 *
 * A narrowing delegate to {@link initialIntentForMode}, which covers all five
 * modes. The narrow signature is what the dialog wants -- it offers three -- and
 * a second copy of the per-mode field lists is what this avoids.
 */
export function changeBranchInitialIntent(mode: ChangeBranchMode): GitModeIntent {
  return initialIntentForMode(mode)
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
        // every other case goes through a fieldsForXxx helper that
        // already spreads EMPTY_GIT_FIELDS, so returning the shared
        // singleton here would make mutation inconsistent.
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
