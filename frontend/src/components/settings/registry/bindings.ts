import type { DualBinding, SettingBinding } from '../types'
import type { DualPreference, FontTier } from '~/context/PreferencesContext'

// The binding factories the declaration table in `./settings` binds its
// entries with: how a row reads its value, which tier a write lands on, and
// how the user moves between the two tiers.
//
// They sit apart from the table because they answer a different question.
// The table states WHICH settings exist and what a user calls them; these
// state HOW a tiered value is read and written, once, for every entry that
// has that shape.

/**
 * The binding a row whose CUSTOM EDITOR owns its own value: none.
 *
 * Seven rows need it -- the six `account.*` rows and `advanced.keyPins`. Each
 * renders a bespoke editor that reads and writes through its own RPCs, so
 * there is no scalar for the registry to bind, and the row's `value`/`set`
 * pair is never called. The registry's base declaration still demands the
 * pair, because every VALUE-BACKED row needs it and a missing one is a dead
 * control; this is the one honest answer for a row that holds no value.
 *
 * ONE shared object, not a fresh literal at each entry. Seven copies of the
 * same two no-op closures say nothing seven times, and a reader has to compare
 * them to learn that they agree. Nothing mutates a binding -- the panel reads
 * `value`, `set` and the optional override members -- so the rows may share it.
 */
export const CUSTOM_EDITOR_OWNS_ITS_VALUE: SettingBinding = {
  value: () => null,
  set: () => {},
}

/**
 * The five members every dual-scope binding carries: which tier the
 * control edits, how the user moves between the tiers, and how the
 * account tier is addressed.
 *
 * They lived inline in the two factories below, which had to agree and
 * did not: the `!overridden()` guard on `customized` reached neither.
 * Extracted once, so a rule stated here holds for both.
 */
function dualOverrideMembers<T>(pref: DualPreference<T>) {
  const overridden = () => pref.browser() !== null
  return {
    overridden,
    clearOverride: () => pref.setBrowser(null),
    beginOverride: () => pref.setBrowser(pref.resolved()),
    /**
     * A row that is EDITING its browser tier must not offer the account
     * tier's Reset. The resolved value is `browser() ?? account()`, so
     * removing the account default leaves the control exactly where it
     * stands — the user destroys their stored default and sees nothing
     * move.
     */
    customized: () => !overridden() && pref.customized(),
    reset: () => pref.reset(),
  }
}

/**
 * A dual-scope scalar: a nullable browser override over an account value
 * backed by one proto key. The scope chip decides which tier the control
 * edits; `customized`/`reset` address the account tier.
 */
export function dualScalar<T>(pref: DualPreference<T>): DualBinding {
  const members = dualOverrideMembers(pref)
  return {
    value: pref.resolved,
    // An account write returns its promise so a refusal reaches the row.
    // A browser write cannot fail.
    set: (v) => {
      if (members.overridden()) {
        pref.setBrowser(v as T)
        return
      }
      return pref.setAccount(v as T)
    },
    ...members,
    // No `resetsWholeKey`: a scalar dual row IS its key, so its Reset
    // removes exactly what the row shows and the plain button is exact.
  }
}

/**
 * One HALF of a dual-scope font tier.
 *
 * The two halves are never written apart. Overriding the toggle and the
 * list independently gives incoherent states, so both tiers treat the
 * whole `{enabled, fonts}` object as the override unit — the Go
 * declaration says the same (`usersettings.FontFamilyValue`). Writing one
 * half therefore has to read the other back, which is the rule this
 * helper holds. It lived inline in four near-identical blocks, so a fix
 * to it could land in three of them and miss the fourth.
 */
export function dualFontHalf(half: 'enabled' | 'fonts', pref: DualPreference<FontTier>): DualBinding {
  const merge = (base: FontTier, v: unknown): FontTier =>
    half === 'enabled'
      ? { enabled: v === true, fonts: base.fonts }
      : { enabled: base.enabled, fonts: Array.isArray(v) ? v as string[] : [] }
  const members = dualOverrideMembers(pref)
  return {
    value: () => pref.resolved()[half],
    set: (v) => {
      if (members.overridden()) {
        pref.setBrowser(merge(pref.browser() ?? { enabled: false, fonts: [] }, v))
        return
      }
      return pref.setAccount(merge(pref.account(), v))
    },
    ...members,
    // The account reset removes the WHOLE `{enabled, fonts}` document —
    // the hub has no per-field reset on the wire — so it takes the OTHER
    // half's value with it. The row has to say what else goes and ask
    // before it goes, which is what naming the key here turns on.
    resetsWholeKey: pref.protoKey,
  }
}

/**
 * A browser-only boolean preference whose setter deletes the key at its
 * default. It takes no `prefs`: a browser tier has no account half to
 * address, so there is nothing for `customized`/`reset` to point at.
 */
export function browserToggle(read: () => boolean, write: (v: boolean) => void): SettingBinding {
  return {
    value: read,
    set: v => write(v === true),
  }
}
