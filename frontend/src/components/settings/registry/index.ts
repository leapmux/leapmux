import type { CategoryId, SettingControl, SettingDescriptor, SettingRowModel } from '../types'
import type { AccountSettingDecl, BrowserOnlySettingDecl } from './settings'
import type { PreferencesState } from '~/context/PreferencesContext'
import type { SettingDescriptor as ProtoSettingDescriptor, SettingField } from '~/generated/proto/leapmux/v1/settings_pb'
import { isSoloMode } from '~/lib/systemInfo'
import { CATEGORY_IDS, controlForField } from '../protoRegistry'
import { browserSettings } from './settings'

// What the dialog reads OFF the declaration table in `./settings`: the row
// each entry renders as, and the parity join the registry test reads. The
// table itself, and the binding factories it uses, stay in their own
// modules — one file that held all three grew past 600 lines and three
// unrelated concerns.

/**
 * The row a BROWSER-ONLY entry renders as.
 *
 * Derived, never re-typed: dropping the declaration-only members leaves
 * exactly a descriptor. A field-by-field copy would drop any descriptor
 * field a later change adds, and the panels and the search index would lose
 * it with no signal.
 */
function browserOnlyDescriptor(decl: BrowserOnlySettingDecl, prefs: PreferencesState): SettingDescriptor {
  const {
    sentinel: _sentinel,
    bind: _bind,
    resetBrowser: _resetBrowser,
    protoKey: _protoKey,
    hiddenWhen,
    ...descriptor
  } = decl
  return {
    ...descriptor,
    hidden: hideWhenAny(descriptor.hidden, preferenceRule(hiddenWhen, prefs)),
  }
}

/**
 * The row an ACCOUNT-BACKED entry renders as, or undefined when this client
 * cannot render one.
 *
 * The join: the hub's field states the SHAPE (category, control kind, enum
 * values, numeric bounds, unit, restart) and the declaration states the
 * PRESENTATION (label, help, keywords, option names, add name) plus the
 * browser tier. Neither side restates the other's half, so the pair cannot
 * drift.
 *
 * Three conditions yield no row, each of them a shape this client cannot
 * show rather than a value it could invent:
 *
 * - the hub declares no such key or field (an older hub, or a key this
 *   registry claims and the hub dropped);
 * - the field's category has no navigation group, so the row has nowhere
 *   to render — the same answer `buildProtoRows` gives a hub row;
 * - the field's control is one this client does not carry, today only a
 *   custom editor that a newer hub declares.
 */
function accountDescriptor(
  decl: AccountSettingDecl,
  wire: WireFields,
  prefs: PreferencesState,
): SettingDescriptor | undefined {
  const found = wire.get(decl.protoKey)?.get(decl.protoField ?? '')
  if (found === undefined)
    return undefined
  if (!CATEGORY_IDS.has(found.category as CategoryId))
    return undefined
  // No `valueOf`: an account field is never secret, so no control this
  // builds reads a live value. `usersettings` declares no secret field, and
  // a secret one could not be edited through the typed account signals in
  // any case.
  const control = controlForField(found.field)
  if (control === undefined)
    return undefined
  return {
    id: decl.id,
    category: found.category as CategoryId,
    label: decl.label,
    help: decl.help,
    keywords: decl.keywords,
    scope: decl.scope,
    control: withDeclaredNames(control, decl),
    restart: found.restart || undefined,
    // The wire's own `dependsOn` is NOT consulted, and it is the one hide
    // rule this path drops: it reads a live value out of a
    // `Map<string, SettingValue>`, which the typed account tier does not
    // keep, while the declaration's `hiddenWhen` reads the parsed signal
    // directly and is exact. `usersettings` declares no `DependsOn` today.
    // BOTH deployment flags ARE honored, because neither needs a value. They
    // are honored HERE and nowhere else: ListUserSettings sends every account
    // descriptor unfiltered, unlike ListSettings, which drops a hub-scope
    // descriptor its own deployment hides. So this join is the account scope's
    // one enforcement point, and half of it would hide half the rows.
    hidden: hideWhenAny(
      decl.hidden,
      found.hiddenInSolo ? () => isSoloMode() : undefined,
      found.hiddenInHub ? () => !isSoloMode() : undefined,
      preferenceRule(decl.hiddenWhen, prefs),
    ),
  }
}

/**
 * Bind a rule that reads the preferences context into one that takes no
 * arguments, which is the only shape a descriptor's `hidden` can hold.
 *
 * `hiddenWhen` cannot be a descriptor member of its own for exactly that
 * reason, and a rule like "the font stack only matters while custom fonts
 * are on" has to read the context to answer.
 */
function preferenceRule(
  hiddenWhen: ((prefs: PreferencesState) => boolean) | undefined,
  prefs: PreferencesState,
): (() => boolean) | undefined {
  return hiddenWhen === undefined ? undefined : () => hiddenWhen(prefs)
}

/**
 * Fold every hide rule into the one `hidden` predicate a descriptor carries.
 * The row hides when ANY of them holds, and an entry with no rule at all
 * carries no predicate.
 *
 * A LIST, never a chain of ternaries, for the reason `buildProtoRows` states
 * beside its own `hideReasons`: picking one rule cancels the rest, and a
 * third rule added later then silently disables the two before it.
 */
function hideWhenAny(...rules: ((() => boolean) | undefined)[]): (() => boolean) | undefined {
  const active = rules.filter(rule => rule !== undefined)
  if (active.length === 0)
    return undefined
  if (active.length === 1)
    return active[0]
  return () => active.some(holds => holds())
}

/**
 * Put the names a user reads onto a control the wire described.
 *
 * The wire's own label WINS wherever it has one, so a hub that starts to
 * declare labels takes over with no edit here. The declaration answers
 * next, and a value that neither side names renders under its own slug — an
 * option the hub added and this registry has not caught up with is still
 * offered and still selectable, only not yet translated.
 */
function withDeclaredNames(control: SettingControl, decl: AccountSettingDecl): SettingControl {
  if (control.kind === 'enum') {
    return {
      kind: 'enum',
      options: control.options.map(option => ({
        ...option,
        label: option.label || decl.optionLabels?.[option.value] || option.value,
      })),
    }
  }
  if (control.kind === 'stringList' && decl.addLabel !== undefined)
    return { kind: 'stringList', addLabel: decl.addLabel }
  return control
}

/** One wire field, with the facts its owning descriptor states. */
interface WireField {
  field: SettingField
  category: string
  hiddenInSolo: boolean
  hiddenInHub: boolean
  restart: boolean
}

/** Wire fields by setting key, then by field name (`''` for a scalar). */
type WireFields = Map<string, Map<string, WireField>>

/**
 * Index the hub's account descriptors for the join.
 *
 * Two levels rather than one `${key}.${field}` string: a settings key may
 * itself contain a dot, so a flat key makes a key `captcha.altcha` with a
 * field `x` indistinguishable from a key `captcha` with a field `altcha.x`.
 * The declarations state the two apart for the same reason.
 */
function indexWireFields(descriptors: readonly ProtoSettingDescriptor[]): WireFields {
  const wire: WireFields = new Map()
  for (const desc of descriptors) {
    let byField = wire.get(desc.key)
    if (byField === undefined) {
      byField = new Map()
      wire.set(desc.key, byField)
    }
    for (const field of desc.fields) {
      byField.set(field.name, {
        field,
        category: desc.category,
        hiddenInSolo: desc.hiddenInSolo,
        hiddenInHub: desc.hiddenInHub,
        restart: desc.restart,
      })
    }
  }
  return wire
}

/**
 * The registry's rows: each entry's descriptor, with every visibility rule
 * resolved, beside the binding that edits it.
 *
 * This is the ONLY derivation, on purpose. A second, static descriptor list
 * beside this one would omit exactly the rules that need the context, which
 * is how a test comes to assert a row set the dialog never renders. Pairing
 * the binding here rather than in a separate id-keyed record is what makes
 * the row set the dialog's ONE unit: the panel receives whole rows, so it
 * cannot look a binding up and miss.
 *
 * `accountDescriptors` is the hub's reply to `ListUserSettings`, which
 * `PreferencesContext` keeps. An ACCOUNT-BACKED entry yields NO ROW until
 * that reply arrives, because its shape is the hub's to state: a failed
 * load renders the account rows not at all, rather than at defaults this
 * client invented, and `PreferencesDialog` says so where the rows would
 * have been. The row set is therefore a function of the reply, so a caller
 * that renders it must build it inside a memo.
 */
export function createBrowserRows(
  prefs: PreferencesState,
  accountDescriptors: readonly ProtoSettingDescriptor[],
): SettingRowModel[] {
  const wire = indexWireFields(accountDescriptors)
  return browserSettings.flatMap((decl) => {
    const descriptor = decl.protoKey === undefined
      ? browserOnlyDescriptor(decl, prefs)
      : accountDescriptor(decl, wire, prefs)
    return descriptor === undefined ? [] : [{ descriptor, binding: decl.bind(prefs) }]
  })
}

/**
 * The user-scope proto keys the browser registry claims.
 *
 * It is a PARITY JOIN, not a runtime filter, and no production code reads
 * it. `index.test.ts` asserts this set against the hub's golden account
 * schema, so a key the hub declares and this registry does not — or the
 * reverse — fails the suite. The dialog needs no filter of its own: only
 * the HUB scope builds store-backed rows, so there is no second row for
 * these keys to remove (`PreferencesDialog` states the same).
 *
 * DERIVED from the entries themselves, never re-typed, because a
 * hand-kept list could only disagree with the registry it describes. The
 * sibling rule is stated for `CATEGORY_IDS` in `./protoRegistry`.
 *
 * The consequence this does NOT prevent: an account key a newer hub
 * declares and this registry never does gets no row at all. It reaches
 * `applyAccountValue` in `PreferencesContext`, which logs and drops it.
 * The parity test above is what turns that silent drop into a build
 * failure.
 */
export const CLAIMED_PROTO_KEYS: ReadonlySet<string> = new Set(
  browserSettings.flatMap(d => (d.protoKey === undefined ? [] : [d.protoKey])),
)
