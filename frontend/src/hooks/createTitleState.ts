import type { Accessor } from 'solid-js'
import { createMemo, createSignal, untrack } from 'solid-js'
import { sanitizeName } from '~/lib/validate'

export interface TitleState {
  /** Current value, exactly as typed. */
  value: Accessor<string>
  setValue: (v: string) => void
  /** Replace the value with a freshly generated one. */
  regenerate: () => void
  /**
   * Re-roll, but only while the field is still PRISTINE — still exactly the
   * string the generator last produced, with nothing typed over it.
   *
   * For a dialog whose generator depends on another control: ChangeBranchDialog
   * picks `Agent <Name>` or `Terminal <Name>` from its Open-as toggle, so
   * flipping that toggle has to re-roll or the tab carries the wrong prefix.
   * Doing it unconditionally would throw away a name the user typed, which is
   * the mistake the non-reactive generator exists to prevent.
   *
   * `regenerate` counts as pristine afterwards: clicking the button leaves the
   * field generator-owned, so a later flip may still re-roll it.
   */
  regenerateIfPristine: () => void
  /**
   * The value the caller must SEND: the cleaned form, not the raw text.
   *
   * The server applies `CleanName` to whatever arrives, so sending the raw
   * text made the dialog show one title while the server stored another until
   * the next refresh overwrote it. The gap widened when the rule started to
   * FOLD whitespace: a plain double space is a far more common typo than a
   * control character was.
   */
  cleaned: Accessor<string>
  /** Validation error for the current value, or null when it is acceptable. */
  error: Accessor<string | null>
  /**
   * True while the value is still exactly what the generator produced — the
   * suggestion this dialog pre-filled, with nothing typed over it.
   *
   * The caller SENDS this alongside the title, because only the client can
   * answer it. A dialog pre-fills `Agent <Name>` from the shared pool and
   * sends it, so what arrives at the worker is indistinguishable from a title
   * the user typed. The worker records the answer (`title_auto_generated`) and
   * plan-mode auto-rename reads it; before the flag it re-derived the answer
   * from the rendered string with a regex, and overwrote a typed `Agent Bob`.
   *
   * It is REACTIVE, unlike the internal comparison `regenerateIfPristine` runs:
   * a submit handler reads it at submit time, so it has to track the value.
   */
  isPristine: Accessor<boolean>
}

/**
 * Reactive state for a dialog's Title field: a generated initial value, a
 * regenerate action, and the one validation rule every title path shares.
 *
 * `generate` is a parameter rather than a fixed pool because the three callers
 * want different names for the same field. New Agent and New Terminal draw
 * `Agent <Name>` / `Terminal <Name>` from the worker's own pool, so a
 * pre-filled title is indistinguishable from the one the worker would have
 * picked. New Workspace draws a word slug, because a workspace carries no
 * prefix and a lone first name would read as an unfinished label.
 *
 * `generate` is called ONCE for the initial value and again per `regenerate`.
 * It is not reactive: a dialog that re-rolled the title on some unrelated
 * signal change would discard what the user typed.
 */
export function createTitleState(generate: () => string): TitleState {
  // Untracked for the reason `setGenerated` states below, and for the same
  // reason at the same strength. A component body runs inside `untrack`, so
  // every caller today is safe by accident; the first one that builds a title
  // inside a `createMemo` or `createEffect` would subscribe that computation
  // to whatever the generator reads.
  const initial = untrack(generate)
  const [value, setValue] = createSignal(initial)
  // The last string the generator produced. The field is PRISTINE while the
  // value still equals it; anything else means the user took the field over.
  //
  // A SIGNAL rather than a plain `let`, because `isPristine` is part of the
  // submit payload and a submit computation has to see it change.
  const [lastGenerated, setLastGenerated] = createSignal(initial)
  const sanitized = createMemo(() => sanitizeName(value()))

  // `generate` is untracked: a caller whose generator reads a signal (the
  // Open-as toggle) would otherwise subscribe whatever effect called this to
  // that signal, on top of the one it already tracks deliberately.
  const setGenerated = () => {
    const next = untrack(generate)
    setLastGenerated(next)
    setValue(next)
  }

  return {
    value,
    setValue,
    regenerate: setGenerated,
    // `value` is untracked for the same reason, and a sharper one: this runs
    // inside an effect keyed on the tab type, and a tracked read would re-run
    // that effect on every keystroke. `lastGenerated` is untracked with it, so
    // the effect subscribes to neither.
    regenerateIfPristine: () => {
      if (untrack(value) === untrack(lastGenerated))
        setGenerated()
    },
    cleaned: () => sanitized().value,
    error: () => sanitized().error,
    // Tracked on purpose, unlike the comparison above. The CLEANED value is
    // what the caller sends, so pristine-ness is measured against the cleaned
    // suggestion too: trailing whitespace the server would fold away is not a
    // choice the user made.
    isPristine: () => sanitizeName(lastGenerated()).value === sanitized().value,
  }
}
