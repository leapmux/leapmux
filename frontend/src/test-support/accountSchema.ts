import type { SettingDescriptor, SettingField } from '~/generated/proto/leapmux/v1/settings_pb'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { SettingFieldKind } from '~/generated/proto/leapmux/v1/settings_pb'

/** One key of the hub's golden account schema, as the file records it. */
export interface GoldenOption { value: string }
export interface GoldenField {
  name: string
  kind: string
  enumValues?: GoldenOption[]
  min?: number
  max?: number
  /**
   * "percent" turns an integer field into a slider; see `controlForField`.
   * Absent for every field the hub declares without one, because the Go
   * side writes the golden with `json:"unit,omitempty"`.
   */
  unit?: string
  /**
   * The client-side editor of a `custom` field. A field whose editor this
   * client does not carry renders no row at all.
   */
  customId?: string
}
export interface GoldenKey { key: string, category: string, title: string, fields: GoldenField[] }

/**
 * The hub's declared account schema, read from the Go side's golden file.
 *
 * The ONE artifact both scopes are pinned to: `TestAccountSchemaMatchesGolden`
 * rewrites it from `usersettings/keys.go`, so a key, field, kind, enum value
 * or bound that moves in Go moves here in the same commit.
 */
export function goldenAccountSchema(): GoldenKey[] {
  return JSON.parse(readFileSync(
    resolve(import.meta.dirname, '../../../backend/internal/hub/usersettings/testdata/account_schema.json'),
    'utf8',
  )) as GoldenKey[]
}

const KIND_BY_GOLDEN_NAME: Record<string, SettingFieldKind> = {
  'bool': SettingFieldKind.BOOL,
  'integer': SettingFieldKind.INT,
  'float': SettingFieldKind.FLOAT,
  'string': SettingFieldKind.STRING,
  'enum': SettingFieldKind.ENUM,
  'string-list': SettingFieldKind.STRING_LIST,
  'bytes': SettingFieldKind.BYTES,
  'custom': SettingFieldKind.CUSTOM,
}

/**
 * The hub's `ListUserSettings` descriptors, as production receives them.
 *
 * Every account row's SHAPE comes off this reply, so a test that renders
 * account rows has to supply it. Built from the golden schema rather than
 * typed out, so a key the hub adds reaches these tests without an edit —
 * and every fact that picks a control comes off the golden too. The `unit`
 * and the `customId` were the two this file used to restate by hand, which
 * meant a fixture that rendered a NUMBER box where production renders a
 * slider, and no keyboard-shortcuts row at all, whenever the hand-kept
 * table fell behind Go.
 *
 * What stays fabricated here is what the hub deliberately does not declare:
 * every label, help string and placeholder is the registry's to supply
 * (see `schema_golden_test.go`), and `secret` cannot be set at all — the
 * account scope refuses a key that declares a secret field at registration.
 */
export function accountWireDescriptors(): SettingDescriptor[] {
  return goldenAccountSchema().map(key => ({
    key: key.key,
    category: key.category,
    title: key.title,
    summary: '',
    hiddenInSolo: false,
    restart: false,
    fields: key.fields.map(field => ({
      name: field.name,
      label: '',
      help: '',
      kind: KIND_BY_GOLDEN_NAME[field.kind] ?? SettingFieldKind.UNSPECIFIED,
      enumValues: (field.enumValues ?? []).map(o => ({ value: o.value, label: '', help: '' })),
      min: field.min === undefined ? undefined : BigInt(field.min),
      max: field.max === undefined ? undefined : BigInt(field.max),
      unit: field.unit ?? '',
      secret: false,
      placeholder: '',
      customId: field.customId ?? '',
    } as unknown as SettingField)),
  }) as unknown as SettingDescriptor)
}
