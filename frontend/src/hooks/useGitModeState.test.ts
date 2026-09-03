import type { GitModeIntent } from '~/hooks/useGitModeState'
import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import {
  CHANGE_BRANCH_MODES,
  changeBranchInitialIntent,
  fieldsForCheckoutBranch,
  fieldsForCreateBranch,
  fieldsForCreateWorktree,
  fieldsForUseWorktree,
  GIT_MODE_LABELS,
  GitMode,
  gitModeFromToken,
  gitModeMenuLabel,
  gitModeToken,
  initialIntentForMode,
  isChangeBranchMode,
  useGitModeState,
} from '~/hooks/useGitModeState'
import { shallowEqual } from '~/lib/shallowEqual'
import { flush } from '~/test-support/async'

// GitOptions uses `shallowEqual` as the createMemo equality callback so
// it dedupes structurally-identical GitModeIntent values. Without it
// the memo would emit a fresh object on every keystroke and the
// parent's onGitModeChange would fire even when the active-mode
// payload didn't change. The tests below pin the variant-by-variant
// equality semantics that GitOptions relies on.

function switchBranch(checkoutBranch: string, checkoutBranchError: string | null = null): GitModeIntent {
  return { mode: GitMode.SwitchBranch, checkoutBranch, checkoutBranchError }
}

function createBranch(overrides: Partial<{
  createBranch: string
  createBranchError: string | null
  createBranchBase: string
}> = {}): GitModeIntent {
  return {
    mode: GitMode.CreateBranch,
    createBranch: overrides.createBranch ?? 'feature-x',
    createBranchError: overrides.createBranchError ?? null,
    createBranchBase: overrides.createBranchBase ?? 'main',
  }
}

function createWorktree(overrides: Partial<{
  worktreeBranch: string
  worktreeBranchError: string | null
  worktreeBaseBranch: string
}> = {}): GitModeIntent {
  return {
    mode: GitMode.CreateWorktree,
    worktreeBranch: overrides.worktreeBranch ?? 'feature-x',
    worktreeBranchError: overrides.worktreeBranchError ?? null,
    worktreeBaseBranch: overrides.worktreeBaseBranch ?? 'main',
  }
}

describe('shallowEqual (GitModeIntent semantics)', () => {
  it('reports unequal across different modes', () => {
    expect(shallowEqual({ mode: GitMode.Current }, switchBranch('main'))).toBe(false)
    expect(shallowEqual(createBranch(), createWorktree())).toBe(false)
    expect(shallowEqual(
      { mode: GitMode.UseWorktree, useWorktreePath: '/wt' },
      { mode: GitMode.Current },
    )).toBe(false)
  })

  it('current variant equals itself (no fields to compare)', () => {
    expect(shallowEqual({ mode: GitMode.Current }, { mode: GitMode.Current })).toBe(true)
  })

  it('switchBranch compares checkoutBranch', () => {
    expect(shallowEqual(switchBranch('main'), switchBranch('main'))).toBe(true)
    expect(shallowEqual(switchBranch('main'), switchBranch('dev'))).toBe(false)
  })

  it('createBranch compares all three payload fields', () => {
    const base = createBranch({ createBranch: 'feat', createBranchBase: 'main', createBranchError: null })
    expect(shallowEqual(base, createBranch({ createBranch: 'feat', createBranchBase: 'main', createBranchError: null }))).toBe(true)
    // Each field flip should break equality independently.
    expect(shallowEqual(base, createBranch({ createBranch: 'other', createBranchBase: 'main', createBranchError: null }))).toBe(false)
    expect(shallowEqual(base, createBranch({ createBranch: 'feat', createBranchBase: 'dev', createBranchError: null }))).toBe(false)
    expect(shallowEqual(base, createBranch({ createBranch: 'feat', createBranchBase: 'main', createBranchError: 'invalid' }))).toBe(false)
  })

  it('createWorktree compares all three payload fields', () => {
    const base = createWorktree()
    expect(shallowEqual(base, createWorktree())).toBe(true)
    expect(shallowEqual(base, createWorktree({ worktreeBranch: 'other' }))).toBe(false)
    expect(shallowEqual(base, createWorktree({ worktreeBaseBranch: 'dev' }))).toBe(false)
    expect(shallowEqual(base, createWorktree({ worktreeBranchError: 'bad' }))).toBe(false)
  })

  it('useWorktree compares useWorktreePath', () => {
    const a: GitModeIntent = { mode: GitMode.UseWorktree, useWorktreePath: '/wt-a' }
    const b: GitModeIntent = { mode: GitMode.UseWorktree, useWorktreePath: '/wt-a' }
    const c: GitModeIntent = { mode: GitMode.UseWorktree, useWorktreePath: '/wt-b' }
    expect(shallowEqual(a, b)).toBe(true)
    expect(shallowEqual(a, c)).toBe(false)
  })

  it('treats null and "" as distinct error states (so a typo-clear fires onGitModeChange)', () => {
    // Regression guard: an empty-string error vs. null error are
    // semantically different to dialogValidation — null means "no
    // validation problem", "" would be a programming bug but must not
    // accidentally compare equal.
    const cleared = createBranch({ createBranchError: null })
    const empty = createBranch({ createBranchError: '' })
    expect(shallowEqual(cleared, empty)).toBe(false)
  })
})

describe('useGitModeState', () => {
  it('seeds to the Current variant when no initial intent is provided', () => {
    const s = useGitModeState()
    expect(s.gitMode()).toBe(GitMode.Current)
    expect(s.currentIntent()).toEqual({ mode: GitMode.Current })
  })

  it('seeds to the provided initial intent so the dialog opens on the right mode', () => {
    // ChangeBranchDialog opens on SwitchBranch (Current is intentionally
    // excluded from its enabled modes). Without this seed the radio
    // would paint Current on first render before GitOptions' on-mount
    // emit overrode it — a one-frame flicker, and also `state.gitMode()`
    // reads in dialog Show conditions would briefly see Current.
    const seed: GitModeIntent = { mode: GitMode.SwitchBranch, checkoutBranch: '', checkoutBranchError: null }
    const s = useGitModeState(seed)
    expect(s.gitMode()).toBe(GitMode.SwitchBranch)
    expect(s.currentIntent()).toEqual(seed)
  })

  it('seeded initial intent propagates through toGitFields', () => {
    // The RPC-payload projection must respect the seed too — a dialog
    // that submits before any user interaction (unlikely but possible)
    // would otherwise ship blank fields despite the visible mode.
    const s = useGitModeState({
      mode: GitMode.CreateBranch,
      createBranch: 'feat',
      createBranchError: null,
      createBranchBase: 'main',
    })
    expect(s.toGitFields()).toMatchObject({
      createBranch: 'feat',
      createBranchBase: 'main',
      createWorktree: false,
      checkoutBranch: '',
    })
  })

  it('handleGitModeChange after a seed swaps the variant cleanly (no leftover seed fields)', () => {
    // Defensive: the signal's `equals: shallowEqual` could mask a
    // setter that forgot to clear a previous variant's fields. The
    // initial-intent path must not be a special case — once
    // handleGitModeChange runs, the new variant is the only truth.
    const s = useGitModeState({ mode: GitMode.SwitchBranch, checkoutBranch: 'main', checkoutBranchError: null })
    s.handleGitModeChange({ mode: GitMode.Current })
    expect(s.gitMode()).toBe(GitMode.Current)
    expect(s.currentIntent()).toEqual({ mode: GitMode.Current })
    expect(s.toGitFields().checkoutBranch).toBe('')
  })

  it('handleGitModeChange swaps the active variant atomically', () => {
    const s = useGitModeState()
    s.handleGitModeChange(switchBranch('main'))
    expect(s.gitMode()).toBe(GitMode.SwitchBranch)
    expect(s.currentIntent()).toEqual({ mode: GitMode.SwitchBranch, checkoutBranch: 'main', checkoutBranchError: null })
  })

  it('toGitFields(Current) blanks every field', () => {
    const s = useGitModeState()
    expect(s.toGitFields()).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: '',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '',
    })
  })

  it('toGitFields(SwitchBranch) only sets checkoutBranch', () => {
    const s = useGitModeState()
    s.handleGitModeChange(switchBranch('feature'))
    expect(s.toGitFields()).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: 'feature',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '',
    })
  })

  it('toGitFields(CreateBranch) sets the branch + base, leaves worktree fields blank', () => {
    const s = useGitModeState()
    s.handleGitModeChange(createBranch({ createBranch: 'feat', createBranchBase: 'main' }))
    expect(s.toGitFields()).toMatchObject({
      createWorktree: false,
      createBranch: 'feat',
      createBranchBase: 'main',
      worktreeBranch: '',
      checkoutBranch: '',
    })
  })

  it('toGitFields(CreateWorktree) flips the createWorktree flag and forwards worktree fields', () => {
    const s = useGitModeState()
    s.handleGitModeChange(createWorktree({ worktreeBranch: 'feat-wt', worktreeBaseBranch: 'main' }))
    expect(s.toGitFields()).toMatchObject({
      createWorktree: true,
      worktreeBranch: 'feat-wt',
      worktreeBaseBranch: 'main',
      checkoutBranch: '',
      createBranch: '',
    })
  })

  it('toGitFields(UseWorktree) only sets useWorktreePath', () => {
    const s = useGitModeState()
    s.handleGitModeChange({ mode: GitMode.UseWorktree, useWorktreePath: '/wt' })
    expect(s.toGitFields()).toMatchObject({
      useWorktreePath: '/wt',
      createWorktree: false,
      createBranch: '',
      checkoutBranch: '',
    })
  })

  it('does not leak fields from a previous variant when switching modes', () => {
    // Regression guard: each variant's projection must clear the
    // other variants' fields so a mode toggle doesn't accidentally
    // submit stale values on the wire.
    const s = useGitModeState()
    s.handleGitModeChange(createBranch({ createBranch: 'old', createBranchBase: 'main' }))
    s.handleGitModeChange(switchBranch('newBranch'))
    expect(s.toGitFields()).toMatchObject({
      checkoutBranch: 'newBranch',
      createBranch: '',
      createBranchBase: '',
    })
  })

  it('gitMode() dedupes across same-mode field updates (memo equality)', async () => {
    // Regression guard for the `gitMode = createMemo(() => currentIntent().mode)`
    // optimization. CreateBranch field-level writes (per-keystroke in the
    // branch-name input) change `currentIntent` but leave `.mode`
    // unchanged. A plain `() => currentIntent().mode` accessor would
    // refire every `<Show when={state.gitMode() === X}>` and every
    // gitMode-keyed effect on every keystroke. The memo's default `===`
    // equality drops same-mode emissions.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const s = useGitModeState()
        let runs = 0
        createEffect(() => {
          void s.gitMode()
          runs++
        })
        await flush()
        expect(runs).toBe(1)

        // Enter CreateBranch — mode changes, expect a refire.
        s.handleGitModeChange(createBranch({ createBranch: 'feat', createBranchBase: 'main' }))
        await flush()
        expect(runs).toBe(2)

        // Per-keystroke field update inside CreateBranch — mode unchanged,
        // gitMode() subscriber must stay quiet.
        s.handleGitModeChange(createBranch({ createBranch: 'featu', createBranchBase: 'main' }))
        s.handleGitModeChange(createBranch({ createBranch: 'featur', createBranchBase: 'main' }))
        s.handleGitModeChange(createBranch({ createBranch: 'feature', createBranchBase: 'main' }))
        await flush()
        expect(runs).toBe(2)

        // Base-branch field change inside CreateBranch — still same mode.
        s.handleGitModeChange(createBranch({ createBranch: 'feature', createBranchBase: 'dev' }))
        await flush()
        expect(runs).toBe(2)

        // Mode flip — must refire.
        s.handleGitModeChange(switchBranch('main'))
        await flush()
        expect(runs).toBe(3)

        dispose()
        done()
      })
    })
  })

  it('handleGitModeChange with a structurally-identical intent does NOT refire downstream effects', async () => {
    // Regression guard for the `{ equals: shallowEqual }` option on the
    // intent signal. Without it, every handleGitModeChange call would
    // notify subscribers even when the payload didn't change — defeating
    // every consumer's own dedup work.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const s = useGitModeState()
        let runs = 0
        createEffect(() => {
          // Track currentIntent identity; bumps on every setIntent call
          // unless the signal's `equals` short-circuits it.
          void s.currentIntent()
          runs++
        })
        await flush()
        expect(runs).toBe(1)

        s.handleGitModeChange(switchBranch('main'))
        await flush()
        expect(runs).toBe(2)

        // Identical-shape intent: equals should drop it.
        s.handleGitModeChange(switchBranch('main'))
        await flush()
        expect(runs).toBe(2)

        // A real change does fire.
        s.handleGitModeChange(switchBranch('dev'))
        await flush()
        expect(runs).toBe(3)

        dispose()
        done()
      })
    })
  })
})

describe('isChangeBranchMode', () => {
  it('admits the three modes the dialog renders', () => {
    expect(isChangeBranchMode(GitMode.SwitchBranch)).toBe(true)
    expect(isChangeBranchMode(GitMode.CreateBranch)).toBe(true)
    expect(isChangeBranchMode(GitMode.CreateWorktree)).toBe(true)
  })

  it('rejects modes the dialog never offers', () => {
    // Defensive: `current` (excluded from CHANGE_BRANCH_MODES) and
    // `use-worktree` (a different dialog's mode) must both fail. A
    // dialog opened against an unseeded useGitModeState would briefly
    // observe Current — the predicate rejects it so submit stays gated.
    expect(isChangeBranchMode(GitMode.Current)).toBe(false)
    expect(isChangeBranchMode(GitMode.UseWorktree)).toBe(false)
  })

  it('exposes exactly the three admitted modes via CHANGE_BRANCH_MODES', () => {
    // Pin the publicly-exposed tuple so a future addition to GitMode
    // requires updating both the predicate and the dialog.
    expect([...CHANGE_BRANCH_MODES].toSorted()).toEqual(
      [GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree].toSorted(),
    )
  })
})

// ----- Per-mode field projections -----
//
// ChangeBranchDialog dispatches to a specific RPC inside a switch on
// `intent.mode` and reads the narrowed intent directly. The fieldsFor*
// helpers are the per-mode projection that fills the openAgent /
// openTerminal field set without leaking the other modes' values. Each
// test below pins exactly which fields the projection writes; missing
// or extra fields would silently change the wire payload, so the
// `toEqual` assertions are intentional (not `toMatchObject`).

describe('fieldsForCheckoutBranch', () => {
  it('only sets checkoutBranch; every other RPC field is blank', () => {
    expect(fieldsForCheckoutBranch({
      mode: GitMode.SwitchBranch,
      checkoutBranch: 'origin/main',
      checkoutBranchError: null,
    })).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: 'origin/main',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '',
    })
  })

  it('forwards an empty target verbatim — the dialog gates submit, not this projection', () => {
    // Defensive: the helper does not own validation. Submit-gating lives
    // in dialogValidation. An empty checkoutBranch must round-trip
    // verbatim so the gating layer can refuse it without a magic
    // sentinel.
    expect(fieldsForCheckoutBranch({
      mode: GitMode.SwitchBranch,
      checkoutBranch: '',
      checkoutBranchError: null,
    }).checkoutBranch).toBe('')
  })
})

describe('fieldsForCreateBranch', () => {
  it('sets branch + base; worktree flag stays false and all worktree fields blank', () => {
    expect(fieldsForCreateBranch({
      mode: GitMode.CreateBranch,
      createBranch: 'feature/x',
      createBranchError: null,
      createBranchBase: 'main',
    })).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: '',
      createBranch: 'feature/x',
      createBranchBase: 'main',
      useWorktreePath: '',
    })
  })

  it('forwards an empty base; the worker treats "" as HEAD', () => {
    // The base-branch picker can legitimately be left blank. The
    // projection must pass that through so the backend's
    // current-branch fallback fires.
    const out = fieldsForCreateBranch({
      mode: GitMode.CreateBranch,
      createBranch: 'feat',
      createBranchError: null,
      createBranchBase: '',
    })
    expect(out.createBranch).toBe('feat')
    expect(out.createBranchBase).toBe('')
  })
})

describe('fieldsForCreateWorktree', () => {
  it('flips createWorktree=true and forwards branch + base', () => {
    expect(fieldsForCreateWorktree({
      mode: GitMode.CreateWorktree,
      worktreeBranch: 'feature/wt',
      worktreeBranchError: null,
      worktreeBaseBranch: 'main',
    })).toEqual({
      createWorktree: true,
      worktreeBranch: 'feature/wt',
      worktreeBaseBranch: 'main',
      checkoutBranch: '',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '',
    })
  })

  it('flips createWorktree=true even when fields are empty (validation is upstream)', () => {
    // This is the discriminator: a CreateWorktree intent must surface
    // createWorktree=true regardless of branch content, so the backend
    // dispatches to the worktree path (the empty branch will then fail
    // worker-side validation with a precise error).
    const out = fieldsForCreateWorktree({
      mode: GitMode.CreateWorktree,
      worktreeBranch: '',
      worktreeBranchError: 'Branch name is required',
      worktreeBaseBranch: '',
    })
    expect(out.createWorktree).toBe(true)
    expect(out.worktreeBranch).toBe('')
  })
})

describe('fieldsForUseWorktree', () => {
  it('only sets useWorktreePath; every other field is blank', () => {
    expect(fieldsForUseWorktree({
      mode: GitMode.UseWorktree,
      useWorktreePath: '/abs/path',
    })).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: '',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '/abs/path',
    })
  })
})

describe('toGitFields projects every active mode onto the RPC field set', () => {
  // toGitFields is what consumers actually call (NewAgentDialog,
  // NewTerminalDialog). Asserting on the concrete payload shape — not
  // on equality with the helper invocation we'd otherwise compare to —
  // catches a refactor that swaps in the wrong helper or projects
  // different fields per mode. Tautological equality with
  // `fieldsForXxx(currentIntent())` would pass even after such a
  // breakage because both sides would route through the same helper.
  it('switchBranch produces only checkoutBranch alongside zeros', () => {
    const s = useGitModeState()
    s.handleGitModeChange(switchBranch('main'))
    expect(s.toGitFields()).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: 'main',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '',
    })
  })

  it('createBranch produces createBranch + createBranchBase alongside zeros', () => {
    const s = useGitModeState()
    s.handleGitModeChange(createBranch({ createBranch: 'feat', createBranchBase: 'main' }))
    expect(s.toGitFields()).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: '',
      createBranch: 'feat',
      createBranchBase: 'main',
      useWorktreePath: '',
    })
  })

  it('createWorktree flips createWorktree=true with worktreeBranch + worktreeBaseBranch', () => {
    const s = useGitModeState()
    s.handleGitModeChange(createWorktree({ worktreeBranch: 'feat-wt', worktreeBaseBranch: 'main' }))
    expect(s.toGitFields()).toEqual({
      createWorktree: true,
      worktreeBranch: 'feat-wt',
      worktreeBaseBranch: 'main',
      checkoutBranch: '',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '',
    })
  })

  it('useWorktree only sets useWorktreePath', () => {
    const s = useGitModeState()
    s.handleGitModeChange({ mode: GitMode.UseWorktree, useWorktreePath: '/wt' })
    expect(s.toGitFields()).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: '',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '/wt',
    })
  })

  it('current mode returns a fresh empty object (not the shared singleton)', () => {
    // Regression guard for the shared EMPTY_GIT_FIELDS reference.
    // Mutating one returned object must NOT leak into the next call.
    const s = useGitModeState()
    const first = s.toGitFields()
    expect(first).toEqual({
      createWorktree: false,
      worktreeBranch: '',
      worktreeBaseBranch: '',
      checkoutBranch: '',
      createBranch: '',
      createBranchBase: '',
      useWorktreePath: '',
    })
    first.checkoutBranch = 'mutated'
    const second = s.toGitFields()
    expect(second.checkoutBranch).toBe('')
    expect(second).not.toBe(first)
  })
})

// The seed the branch context menu's three items hand ChangeBranchDialog. It
// carries the MODE alone -- GitOptions owns every field and emits a complete
// intent on its first flush -- so each item paints its own radio immediately
// instead of flashing SwitchBranch first.
describe('changeBranchInitialIntent', () => {
  it('returns the matching variant for each mode', () => {
    expect(changeBranchInitialIntent(GitMode.SwitchBranch)).toEqual({
      mode: GitMode.SwitchBranch,
      checkoutBranch: '',
      checkoutBranchError: null,
    })
    expect(changeBranchInitialIntent(GitMode.CreateBranch)).toEqual({
      mode: GitMode.CreateBranch,
      createBranch: '',
      createBranchError: null,
      createBranchBase: '',
    })
    expect(changeBranchInitialIntent(GitMode.CreateWorktree)).toEqual({
      mode: GitMode.CreateWorktree,
      worktreeBranch: '',
      worktreeBranchError: null,
      worktreeBaseBranch: '',
    })
  })

  it('covers every mode the dialog offers', () => {
    for (const mode of CHANGE_BRANCH_MODES)
      expect(changeBranchInitialIntent(mode).mode).toBe(mode)
  })

  // The seed feeds `useGitModeState`, whose `toGitFields` must not send a
  // half-built payload if the dialog is submitted before GitOptions emits.
  it('projects to blank git fields', () => {
    for (const mode of CHANGE_BRANCH_MODES) {
      const fields = useGitModeState(changeBranchInitialIntent(mode)).toGitFields()
      expect(fields.checkoutBranch).toBe('')
      expect(fields.createBranch).toBe('')
      expect(fields.worktreeBranch).toBe('')
      expect(fields.createWorktree).toBe(mode === GitMode.CreateWorktree)
    }
  })

  it('mints a fresh object per call, so a mutation cannot leak', () => {
    const first = changeBranchInitialIntent(GitMode.CreateBranch)
    expect(changeBranchInitialIntent(GitMode.CreateBranch)).not.toBe(first)
  })
})

// ----- Labels and the serialized token -----
//
// Two tables keyed by GitMode, and both are exhaustive by construction
// (`Record<GitMode, …>`). The pinned tuple below is what makes THAT claim
// checkable from a test: a sixth mode fails the compile at the table and this
// tuple, so neither can be updated alone.
//
// Do NOT iterate `Object.values(GitMode)` instead. An unannotated numeric enum
// emits reverse mappings, so it yields TEN entries -- five names and five
// numbers -- and type-checks clean as `GitMode[]`.
const ALL_GIT_MODES = [
  GitMode.Current,
  GitMode.SwitchBranch,
  GitMode.CreateBranch,
  GitMode.CreateWorktree,
  GitMode.UseWorktree,
] as const

describe('the mode label table (GIT_MODE_LABELS)', () => {
  it('covers every mode of the pinned tuple', () => {
    expect(Object.keys(GIT_MODE_LABELS).length).toBe(ALL_GIT_MODES.length)
    for (const mode of ALL_GIT_MODES)
      expect(GIT_MODE_LABELS[mode]).toBeTruthy()
  })

  it('gives every mode a distinct name', () => {
    const labels = ALL_GIT_MODES.map(m => GIT_MODE_LABELS[m])
    expect(new Set(labels).size).toBe(labels.length)
  })
})

describe('gitModeMenuLabel', () => {
  it('is the radio label plus the three dots a dialog-opening item carries', () => {
    for (const mode of ALL_GIT_MODES)
      expect(gitModeMenuLabel(mode)).toBe(`${GIT_MODE_LABELS[mode]}...`)
  })

  it('uses ASCII dots, not an ellipsis character', () => {
    expect(gitModeMenuLabel(GitMode.SwitchBranch).endsWith('...')).toBe(true)
    expect(gitModeMenuLabel(GitMode.SwitchBranch)).not.toContain('\u2026')
  })
})

describe('gitModeToken / gitModeFromToken', () => {
  it('round-trips every mode', () => {
    for (const mode of ALL_GIT_MODES)
      expect(gitModeFromToken(gitModeToken(mode))).toBe(mode)
  })

  it('mints a distinct token per mode', () => {
    const tokens = ALL_GIT_MODES.map(m => gitModeToken(m))
    expect(new Set(tokens).size).toBe(tokens.length)
  })

  it('never stores the enum NUMBER, which reordering would invalidate', () => {
    for (const mode of ALL_GIT_MODES)
      expect(Number.isNaN(Number(gitModeToken(mode)))).toBe(true)
  })

  it('refuses anything a previous build or a hand edit could have left behind', () => {
    expect(gitModeFromToken(undefined)).toBeUndefined()
    expect(gitModeFromToken(null)).toBeUndefined()
    expect(gitModeFromToken('')).toBeUndefined()
    expect(gitModeFromToken('no-such-mode')).toBeUndefined()
    // A stored NUMBER is the exact shape this type exists to reject: it would
    // resolve to a different mode after the enum is reordered.
    expect(gitModeFromToken(0)).toBeUndefined()
    expect(gitModeFromToken('0')).toBeUndefined()
    expect(gitModeFromToken({ mode: 'current' })).toBeUndefined()
  })
})

describe('initialIntentForMode', () => {
  it('returns an intent carrying the requested mode, for all five', () => {
    for (const mode of ALL_GIT_MODES)
      expect(initialIntentForMode(mode).mode).toBe(mode)
  })

  it('leaves every value field empty, because GitOptions owns them', () => {
    for (const mode of ALL_GIT_MODES) {
      const intent = initialIntentForMode(mode) as Record<string, unknown>
      for (const [key, value] of Object.entries(intent)) {
        if (key === 'mode')
          continue
        expect(value === '' || value === null).toBe(true)
      }
    }
  })

  it('agrees with changeBranchInitialIntent on the three modes they share', () => {
    for (const mode of CHANGE_BRANCH_MODES)
      expect(changeBranchInitialIntent(mode)).toEqual(initialIntentForMode(mode))
  })
})
