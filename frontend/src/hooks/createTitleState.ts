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
  const initial = generate()
  const [value, setValue] = createSignal(initial)
  // The last string the generator produced. The field is PRISTINE while the
  // value still equals it; anything else means the user took the field over.
  let lastGenerated = initial
  const sanitized = createMemo(() => sanitizeName(value()))

  // `generate` is untracked: a caller whose generator reads a signal (the
  // Open-as toggle) would otherwise subscribe whatever effect called this to
  // that signal, on top of the one it already tracks deliberately.
  const setGenerated = () => {
    lastGenerated = untrack(generate)
    setValue(lastGenerated)
  }

  return {
    value,
    setValue,
    regenerate: setGenerated,
    // `value` is untracked for the same reason, and a sharper one: this runs
    // inside an effect keyed on the tab type, and a tracked read would re-run
    // that effect on every keystroke.
    regenerateIfPristine: () => {
      if (untrack(value) === lastGenerated)
        setGenerated()
    },
    cleaned: () => sanitized().value,
    error: () => sanitized().error,
  }
}
