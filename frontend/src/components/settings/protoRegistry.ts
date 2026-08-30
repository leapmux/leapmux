import type { CategoryId, HubBinding, SettingControl, SettingRowModel } from './types'
import type { SettingDescriptor as ProtoSettingDescriptor, SettingField, SettingValue } from '~/generated/proto/leapmux/v1/settings_pb'
import { SettingFieldKind } from '~/generated/proto/leapmux/v1/settings_pb'
import { parseSettingJson } from '~/lib/settingJson'
import { isSoloMode } from '~/lib/systemInfo'
import { NAV_GROUPS } from './navGroups'
import { isCustomEditorId } from './types'

/**
 * The category ids the dialog's navigation knows how to place.
 *
 * DERIVED from the navigation itself, never re-typed: the nav is what
 * decides whether a category has anywhere to render, so a hand-copied
 * third list (beside the `CategoryId` union and `NAV_GROUPS`) could only
 * disagree with it.
 */
export const CATEGORY_IDS: ReadonlySet<CategoryId> = new Set(NAV_GROUPS.map(g => g.category))

/**
 * The RPC surface a proto-backed row reads and writes. Both the user-scope
 * store (userClient) and the hub-scope store (adminClient) supply one;
 * server-first semantics live in the stores, the mapping here is pure.
 */
export interface ProtoSettingsSource {
  /** Live values by setting key. */
  values: () => Map<string, SettingValue>
  /** Merge a partial JSON document onto one key (server-first). */
  update: (key: string, partialJson: string) => Promise<void>
  /** Merge a partial JSON document onto one key's SECRET half. */
  updateSecret: (key: string, partialJson: string) => Promise<void>
  /** Remove one key's stored value (server-first). */
  reset: (key: string) => Promise<void>
}

/** One rendered row built from an RPC descriptor (+ one of its fields). */
export interface ProtoSettingRow extends SettingRowModel {
  /** The setting key on the wire. Object-shaped keys share this across fields. */
  protoKey: string
}

// The safe integer range as bigints, hoisted rather than rebuilt for each
// limit of each field of each row.
const MAX_SAFE = BigInt(Number.MAX_SAFE_INTEGER)
const MIN_SAFE = BigInt(Number.MIN_SAFE_INTEGER)

/**
 * One int64 limit as a JavaScript number, CLAMPED FOR DISPLAY.
 *
 * A proto int64 decodes as a bigint, and `Number` on one past
 * `Number.MAX_SAFE_INTEGER` rounds SILENTLY: a limit of 2^53+1 becomes
 * 2^53, and the number control then refuses a value the hub accepts (or
 * offers one it refuses). The clamp to the safe range keeps the control
 * inside what a JavaScript number states exactly.
 *
 * The clamp moves the CONTROL's limit, never the setting's: the hub holds
 * the authoritative int64 limit and validates every write against it, so a
 * value between the clamp and the real limit is refused by the hub, not
 * accepted silently here.
 */
function limitToNumber(v: bigint | undefined): number | undefined {
  if (v === undefined)
    return undefined
  return Number(v > MAX_SAFE ? MAX_SAFE : v < MIN_SAFE ? MIN_SAFE : v)
}

/**
 * Map one proto field kind + shape onto the frontend control model.
 *
 * `valueOf` is an ACCESSOR, and every control this builds keeps it as one.
 * A control is built while the row set is, so reading a setting value here
 * would make the enclosing row memo depend on EVERY setting value: one
 * successful write would then rebuild every row object, and `<For>`
 * reconciles by reference identity, so it would re-create the DOM of every
 * visible row — including the control that issued the write. A keyboard
 * user who flips a toggle with Space would lose focus to the document
 * body.
 */
export function controlForField(
  field: SettingField,
  valueOf: () => SettingValue | undefined = () => undefined,
): SettingControl | undefined {
  // SECRET FIRST, before the kind is consulted at all. A secret's stored
  // value never crosses the wire — `Redacted` replaces it with the
  // `<redacted>` placeholder — so any control that renders `effectiveJson`
  // shows that placeholder as if it were the value, and commits it back on
  // blur. The ALTCHA signing key is declared BYTES, so testing
  // `field.secret` inside the STRING case alone put the hub's signing key
  // in a plain text box.
  if (field.secret)
    return { kind: 'secret', isSet: () => valueOf()?.secretSet[field.name] === true }
  switch (field.kind) {
    case SettingFieldKind.BOOL:
      return { kind: 'toggle' }
    case SettingFieldKind.INT: {
      const min = limitToNumber(field.min)
      const max = limitToNumber(field.max)
      if (field.unit === '%' || field.unit === 'percent')
        return { kind: 'slider', min: min ?? 0, max: max ?? 100, step: 1, unit: '%' }
      return { kind: 'number', min, max, step: 1, unit: field.unit || undefined }
    }
    case SettingFieldKind.FLOAT:
      return {
        kind: 'number',
        min: field.minF,
        max: field.maxF,
        step: 0.05,
        unit: field.unit || undefined,
      }
    case SettingFieldKind.STRING:
      return { kind: 'text', placeholder: field.placeholder || undefined }
    case SettingFieldKind.ENUM:
      return {
        kind: 'enum',
        options: field.enumValues.map(v => ({ value: v.value, label: v.label, help: v.help || undefined })),
      }
    case SettingFieldKind.STRING_LIST:
      return { kind: 'stringList', addLabel: 'Add' }
    case SettingFieldKind.CUSTOM:
      // The customId is an untyped wire string. A newer hub can declare an
      // editor this client does not carry, and there is no honest fallback:
      // a FieldCustom value is opaque, so a text box would let the user
      // overwrite a structured value with a string. Drop the field instead,
      // the same answer an unknown category gets. Casting the string to
      // CustomEditorId here instead produced a row with a label, help text,
      // and no control at all.
      return isCustomEditorId(field.customId) ? { kind: 'custom', id: field.customId } : undefined
    default:
      // BYTES and anything a newer hub sends: render as text so an unknown
      // kind degrades instead of disappearing.
      return { kind: 'text' }
  }
}

/**
 * Read one JSON address inside a parsed document: the whole value when
 * `field` is empty, else its `field` property. A missing int/float on a
 * present object is 0 — Go json omitempty drops numeric zeros, and the
 * preferences number control must show that 0 rather than an empty field.
 */
function fieldFromParsed(parsed: unknown, field: string, kind?: SettingFieldKind): unknown {
  if (field === '')
    return parsed
  if (parsed === null || typeof parsed !== 'object')
    return undefined
  const current = (parsed as Record<string, unknown>)[field]
  if (current !== undefined)
    return current
  if (kind === SettingFieldKind.INT || kind === SettingFieldKind.FLOAT)
    return 0
  return undefined
}

/**
 * The CONFIGURED document of one wire value: the stored row merged onto the
 * code default, which is the value an edit changes.
 *
 * One wire value carries two documents a row can read, and they are not
 * interchangeable. `merged_json` is what the operator configured;
 * `effective_json` is what the hub enforces right now, which a read-time
 * rule can replace for the life of the process without storing anything
 * (dev mode holds sign-up open, a queue budget of 0 auto-sizes from the
 * process memory limit, an incomplete captcha selection degrades to
 * another provider). The applied value is a fact to REPORT — see
 * `binding.effective` — never a value to edit.
 *
 * The ACCOUNT scope sends no merged document, and it carries no read-time
 * rule, so its effective value already IS its configured one. That makes
 * the fallback exact rather than a guess. An absent field falls back for
 * the same reason a `''` one does: no merged document arrived.
 */
function configuredJson(value: SettingValue): string {
  return value.mergedJson || value.effectiveJson
}

/**
 * Read one JSON address inside the live values' CONFIGURED documents: the
 * whole value of `key` when `field` is empty, else its `field` property.
 * Used both by rows (their slice of the value) and by dependsOn conditions.
 */
function readConfiguredAt(values: Map<string, SettingValue>, key: string, field: string, kind?: SettingFieldKind): unknown {
  const value = values.get(key)
  if (value === undefined)
    return undefined
  return fieldFromParsed(parseSettingJson(configuredJson(value)), field, kind)
}

/**
 * Whether a dependsOn condition currently holds: the value that the condition
 * points at (a sibling field of the same setting when `condition.key` is
 * empty, else the given key's scalar or field) is one of `in`. An absent
 * condition always holds.
 *
 * It reads the CONFIGURED document, the same one every control shows. A
 * hide rule that followed the applied value could hide the very row that
 * repairs it: an incomplete captcha selection degrades at read time, so a
 * credential field hidden until its provider is the applied one would
 * never be fillable.
 */
export function conditionHolds(
  condition: SettingField['dependsOn'],
  key: string,
  values: Map<string, SettingValue>,
): boolean {
  if (!condition)
    return true
  // An empty `key` addresses a sibling field of the same setting; a set key
  // addresses another setting's scalar (field empty) or property.
  const current = readConfiguredAt(values, condition.key || key, condition.field)
  return condition.in.includes(String(current))
}

/**
 * Map RPC descriptors into the frontend row model against a live source.
 *
 * A scalar-shaped descriptor (one field with an empty name) yields one row;
 * an object-shaped descriptor yields one row PER FIELD, each writing a
 * single-property partial document — which is exactly the merge granularity
 * the Update RPC declares.
 */
export function buildProtoRows(
  descriptors: ProtoSettingDescriptor[],
  source: ProtoSettingsSource,
): ProtoSettingRow[] {
  const rows: ProtoSettingRow[] = []
  for (const desc of descriptors) {
    if (!CATEGORY_IDS.has(desc.category as CategoryId))
      continue
    for (const field of desc.fields) {
      const control = controlForField(field, () => source.values().get(desc.key))
      // A field this client cannot render at all — today only a custom
      // editor it does not carry. Dropping it here, before the bindings are
      // built, keeps the row set to what the dialog can actually show.
      if (control === undefined)
        continue
      const scalar = field.name === ''
      const id = scalar ? desc.key : `${desc.key}.${field.name}`
      // The CONTROL edits, so it binds to the configured document. Binding
      // it to the effective one made the note beneath it a tautology — the
      // note printed the control's own value straight back, and the
      // configured value appeared nowhere on screen.
      const valueOf = () => readConfiguredAt(source.values(), desc.key, field.name, field.kind)
      const storedDoc = () => {
        const value = source.values().get(desc.key)
        if (value === undefined || value.valueJson === '')
          return undefined
        return parseSettingJson(value.valueJson)
      }
      // Customized reads THIS FIELD, never the key. The key-level flag
      // marked every field of an object-shaped setting as customized, so
      // all six SMTP rows offered a Reset although one field held the only
      // stored value. A secret has no stored document to read, so its own
      // `secretSet` entry is the answer.
      const customized = () => {
        const value = source.values().get(desc.key)
        if (value === undefined)
          return false
        if (scalar)
          return value.customized === true
        if (field.secret)
          return value.secretSet[field.name] === true
        const stored = storedDoc()
        return typeof stored === 'object' && stored !== null
          && Object.hasOwn(stored as Record<string, unknown>, field.name)
      }
      const binding: HubBinding = {
        value: valueOf,
        set: (v) => {
          const partial = scalar ? v : { [field.name]: v }
          return source.update(desc.key, JSON.stringify(partial))
        },
        customized,
        // The reset RPC removes the key's whole stored row; there is no
        // per-field reset on the wire. A scalar row IS the key, so its
        // reset is exact. A field row must state what else goes with it,
        // which is what `resetsWholeKey` makes the row do.
        reset: () => source.reset(desc.key).then(() => undefined),
        resetsWholeKey: scalar ? undefined : desc.key,
      }
      if (field.secret) {
        binding.set = (v) => {
          const partial = scalar ? v : { [field.name]: v }
          return source.updateSecret(desc.key, JSON.stringify(partial))
        }
      }
      const effectiveAt = () => {
        const value = source.values().get(desc.key)
        if (value === undefined)
          return undefined
        return fieldFromParsed(parseSettingJson(value.effectiveJson), field.name, field.kind)
      }
      // The value the hub ENFORCES, and only while a read-time rule made it
      // differ from the configured value the control carries. The two
      // documents are the operands, never the stored row: an uncustomized
      // key and an object field the operator never touched both have NO
      // stored value, so a comparison against the stored document would
      // print the plain code default on every such row as if the hub had
      // overridden something.
      binding.effective = () => {
        const configured = valueOf()
        const applied = effectiveAt()
        return JSON.stringify(configured) === JSON.stringify(applied) ? undefined : applied
      }
      // Every hide rule is a separate reason, and the row hides when ANY of
      // them holds. A chain of ternaries would let the first reason cancel
      // the rest: a captcha key declares HiddenInSolo AND a per-field
      // dependsOn, so the solo branch alone left every inactive provider's
      // fields on screen. A third rule cannot silently cancel these two.
      const hideReasons: (() => boolean)[] = []
      if (desc.hiddenInSolo)
        hideReasons.push(() => isSoloMode())
      const dependsOn = field.dependsOn
      if (dependsOn)
        hideReasons.push(() => !conditionHolds(dependsOn, desc.key, source.values()))
      rows.push({
        protoKey: desc.key,
        descriptor: {
          id,
          category: desc.category as CategoryId,
          label: field.label || desc.title,
          help: field.help || desc.summary || undefined,
          // The setting's own title, so a search for the object name finds
          // every one of its fields even when each field is labelled apart.
          keywords: [desc.title],
          scope: 'hub',
          control,
          restart: desc.restart || undefined,
          // NO `needsElevation` here. `scope: 'hub'` above already answers it,
          // and `descriptorNeedsElevation` reads the scope: the hub requires an
          // elevated session for every settings write rather than for one key
          // at a time -- a hub setting is deployment-wide, and several of them are
          // the hub's own security controls (sign-up, captcha, the rate limits,
          // SMTP, and the public_url the passkey relying party derives from).
          // See AdminSettingsService.requireElevatedWriter.
          //
          // Stating it from the SCOPE is what the hub does too, and it leaves
          // no per-key flag that a new key can be added without.
          hidden: hideReasons.length === 0
            ? undefined
            : () => hideReasons.some(holds => holds()),
        },
        binding,
      })
    }
  }
  return rows
}
