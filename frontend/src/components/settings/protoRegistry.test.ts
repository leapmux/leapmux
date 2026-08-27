import type { ProtoSettingsSource } from './protoRegistry'
import type { CategoryId, SettingControl } from './types'
import type { PreferencesState } from '~/context/PreferencesContext'
import type { SettingDescriptor as ProtoSettingDescriptor, SettingField, SettingFieldCondition, SettingValue } from '~/generated/leapmux/v1/settings_pb'
import { describe, expect, it, vi } from 'vitest'
import { SettingFieldKind } from '~/generated/leapmux/v1/settings_pb'
import { accountWireDescriptors } from '~/test-support/accountSchema'
import { makeFakePrefs } from '~/test-support/preferencesFake'
import { AccountEmail } from './account/AccountEmail'
import { AccountPasskeys } from './account/AccountPasskeys'
import { AccountProfile } from './account/AccountProfile'
import { CUSTOM_EDITORS } from './controls/customEditors'
import { NAV_GROUPS } from './navGroups'
import { buildProtoRows, CATEGORY_IDS, conditionHolds, controlForField } from './protoRegistry'
import { createBrowserRows } from './registry'
import { browserSettings } from './registry/settings'
import { CUSTOM_EDITOR_IDS } from './types'

// The solo-mode gate is the one environment dependency the hidden() closures
// read; control it per-test rather than depending on the fabricated default.
const isSoloMode = vi.hoisted(() => vi.fn(() => false))
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode,
  isDesktopApp: vi.fn(() => false),
}))

// The proto messages carry a `$typeName` brand; fixtures are plain literals
// cast through unknown so each helper stays readable.
function field(overrides: Partial<SettingField> = {}): SettingField {
  return {
    name: '',
    label: 'Test',
    help: '',
    kind: SettingFieldKind.STRING,
    enumValues: [],
    unit: '',
    secret: false,
    placeholder: '',
    customId: '',
    ...overrides,
  } as unknown as SettingField
}

function descriptor(overrides: Partial<ProtoSettingDescriptor> = {}): ProtoSettingDescriptor {
  return {
    key: 'test_key',
    category: 'general',
    title: 'Test',
    summary: '',
    order: 10,
    hiddenInSolo: false,
    restart: false,
    fields: [field()],
    ...overrides,
  } as unknown as ProtoSettingDescriptor
}

/**
 * One wire value. `mergedJson` defaults to the ACCOUNT scope's shape — the
 * empty string — because that scope sends no merged document at all.
 *
 * The field must stay in this literal. Omitting it left `mergedJson`
 * undefined at runtime, and `undefined !== ''` sent every read down the
 * `JSON.parse(undefined)` path, which throws and resolves to undefined. The
 * merged value was then ALWAYS undefined, so every comparison against it
 * differed and the `effective()` tests below passed with the production
 * comparison reverted.
 */
function value(key: string, effectiveJson: string, overrides: Partial<SettingValue> = {}): SettingValue {
  return {
    key,
    valueJson: '',
    effectiveJson,
    mergedJson: '',
    customized: false,
    secretSet: {},
    ...overrides,
  } as unknown as SettingValue
}

function source(values = new Map<string, SettingValue>()): ProtoSettingsSource {
  return {
    values: () => values,
    update: vi.fn(async () => {}),
    updateSecret: vi.fn(async () => {}),
    reset: vi.fn(async () => {}),
  }
}

/**
 * `controlForField` returns undefined for a field this client cannot render.
 * Every case below expects a control, so assert that here instead of
 * spreading `!` across the file: a regression to undefined then identifies
 * the line that lost it.
 */
function mustControl(...args: Parameters<typeof controlForField>): SettingControl {
  const control = controlForField(...args)
  expect(control, 'the field must produce a control').toBeDefined()
  return control!
}

describe('controlForField', () => {
  it('maps bool to toggle', () => {
    expect(mustControl(field({ kind: SettingFieldKind.BOOL })).kind).toBe('toggle')
  })

  it('maps int with a percent unit to a slider, other ints to number', () => {
    expect(controlForField(field({ kind: SettingFieldKind.INT, unit: '%' })))
      .toMatchObject({ kind: 'slider', min: 0, max: 100, step: 1, unit: '%' })
    expect(controlForField(field({ kind: SettingFieldKind.INT, unit: 'percent' })))
      .toMatchObject({ kind: 'slider', min: 0, max: 100, step: 1, unit: '%' })
    expect(controlForField(field({ kind: SettingFieldKind.INT, unit: 'seconds', min: 300n, max: 604800n })))
      .toMatchObject({ kind: 'number', min: 300, max: 604800, step: 1, unit: 'seconds' })
  })

  // A proto int64 limit decodes as a bigint, and `Number` on one past
  // 2^53-1 rounds SILENTLY: the control would then accept a value the hub
  // refuses, or refuse one it accepts. The clamp keeps the number control
  // inside the range JavaScript can represent exactly, and the hub's own
  // int64 limit is what actually decides the write.
  it('clamps an int limit that JavaScript cannot represent exactly', () => {
    const huge = BigInt(Number.MAX_SAFE_INTEGER) + 1000n
    expect(controlForField(field({ kind: SettingFieldKind.INT, unit: 'bytes', min: -huge, max: huge })))
      .toMatchObject({ kind: 'number', min: Number.MIN_SAFE_INTEGER, max: Number.MAX_SAFE_INTEGER })

    // The boundary itself is representable and must pass through untouched.
    expect(controlForField(field({
      kind: SettingFieldKind.INT,
      unit: 'bytes',
      min: BigInt(Number.MIN_SAFE_INTEGER),
      max: BigInt(Number.MAX_SAFE_INTEGER),
    }))).toMatchObject({ min: Number.MIN_SAFE_INTEGER, max: Number.MAX_SAFE_INTEGER })

    expect(controlForField(field({ kind: SettingFieldKind.INT, unit: 'seconds', min: 0n, max: 3600n })))
      .toMatchObject({ min: 0, max: 3600 })
  })

  // The percent case reads the same clamped limits, so an unrepresentable
  // one must not reach the slider either.
  it('clamps an unrepresentable limit on the percent slider too', () => {
    expect(controlForField(field({
      kind: SettingFieldKind.INT,
      unit: '%',
      max: BigInt(Number.MAX_SAFE_INTEGER) * 4n,
    }))).toMatchObject({ kind: 'slider', min: 0, max: Number.MAX_SAFE_INTEGER })
  })

  it('maps float to number with step 0.05', () => {
    expect(controlForField(field({ kind: SettingFieldKind.FLOAT, minF: 0, maxF: 1 })))
      .toMatchObject({ kind: 'number', min: 0, max: 1, step: 0.05 })
  })

  it('maps string to text with placeholder, secret string to secret', () => {
    expect(controlForField(field({ kind: SettingFieldKind.STRING, placeholder: 'https://hub.example.com' })))
      .toMatchObject({ kind: 'text', placeholder: 'https://hub.example.com' })
    const secret = mustControl(field({ name: 'password', secret: true }), () => value('smtp', '', { secretSet: { password: true } }))
    expect(secret.kind).toBe('secret')
    expect(secret.kind === 'secret' && secret.isSet()).toBe(true)
    const unset = mustControl(field({ name: 'password', secret: true }), () => value('smtp', ''))
    expect(unset.kind === 'secret' && unset.isSet()).toBe(false)
  })

  it('maps enum to enum with options, stringList to stringList, custom by customId', () => {
    expect(controlForField(field({
      kind: SettingFieldKind.ENUM,
      enumValues: [
        { value: 'starttls', label: 'STARTTLS', help: 'upgrade' },
        { value: 'implicit', label: 'Implicit', help: '' },
      ] as SettingField['enumValues'],
    }))).toMatchObject({
      kind: 'enum',
      options: [
        { value: 'starttls', label: 'STARTTLS', help: 'upgrade' },
        { value: 'implicit', label: 'Implicit', help: undefined },
      ],
    })
    expect(mustControl(field({ kind: SettingFieldKind.STRING_LIST })).kind).toBe('stringList')
    expect(controlForField(field({ kind: SettingFieldKind.CUSTOM, customId: 'keybindings' })))
      .toMatchObject({ kind: 'custom', id: 'keybindings' })
  })

  it('degrades unknown kinds to text rather than dropping the field', () => {
    expect(mustControl(field({ kind: SettingFieldKind.BYTES })).kind).toBe('text')
  })
})

describe('buildProtoRows', () => {
  it('yields one row per field of object descriptors, writing per-field partials', async () => {
    const src = source(new Map([['timeouts', value('timeouts', '{"api_seconds":10,"agent_startup_seconds":300}')]]))
    const rows = buildProtoRows([descriptor({
      key: 'timeouts',
      category: 'limits',
      title: 'Timeouts',
      fields: [
        field({ name: 'api_seconds', label: 'API timeout', kind: SettingFieldKind.INT, unit: 'seconds', min: 1n }),
        field({ name: 'agent_startup_seconds', label: 'Agent startup', kind: SettingFieldKind.INT, unit: 'seconds', min: 1n }),
      ],
    })], src)

    expect(rows.map(r => r.descriptor.id)).toEqual(['timeouts.api_seconds', 'timeouts.agent_startup_seconds'])
    expect(rows.map(r => r.protoKey)).toEqual(['timeouts', 'timeouts'])
    expect(rows[0].binding.value()).toBe(10)
    await rows[0].binding.set(15)
    expect(src.update).toHaveBeenCalledWith('timeouts', '{"api_seconds":15}')
  })

  it('scalar descriptors write the bare value', async () => {
    const src = source(new Map([['session_duration_seconds', value('session_duration_seconds', '604800')]]))
    const rows = buildProtoRows([descriptor({
      key: 'session_duration_seconds',
      category: 'general',
      fields: [field({ label: 'Session duration', kind: SettingFieldKind.INT, unit: 'seconds', min: 300n })],
    })], src)
    expect(rows[0].descriptor.id).toBe('session_duration_seconds')
    await rows[0].binding.set(3600)
    expect(src.update).toHaveBeenCalledWith('session_duration_seconds', '3600')
  })

  it('secret fields write through updateSecret', async () => {
    const src = source(new Map([['smtp', value('smtp', '{}')]]))
    const rows = buildProtoRows([descriptor({
      key: 'smtp',
      category: 'email',
      fields: [field({ name: 'password', label: 'Password', secret: true })],
    })], src)
    await rows[0].binding.set('hunter2')
    expect(src.updateSecret).toHaveBeenCalledWith('smtp', '{"password":"hunter2"}')
    expect(src.update).not.toHaveBeenCalled()
  })

  it('depends_on hides sibling fields that do not hold an allowed value', () => {
    const values = new Map([['smtp', value('smtp', '{"tls_mode":"none"}')]])
    const src = source(values)
    const rows = buildProtoRows([descriptor({
      key: 'smtp',
      category: 'email',
      fields: [
        field({ name: 'username', label: 'Username' }),
        field({
          name: 'host',
          label: 'Host',
          dependsOn: { key: '', field: 'tls_mode', in: ['starttls', 'implicit'] } as SettingField['dependsOn'],
        }),
      ],
    })], src)

    expect(rows[0].descriptor.hidden?.() ?? false).toBe(false)
    expect(rows[1].descriptor.hidden?.() ?? false).toBe(true)

    values.set('smtp', value('smtp', '{"tls_mode":"starttls"}'))
    expect(rows[1].descriptor.hidden?.() ?? false).toBe(false)
  })

  it('depends_on can address another setting key', () => {
    const values = new Map([
      ['signup_enabled', value('signup_enabled', 'true')],
      ['some_other_setting', value('some_other_setting', 'false')],
    ])
    expect(conditionHolds({ key: 'signup_enabled', field: '', in: ['true'] } as SettingFieldCondition, 'other', values)).toBe(true)
    expect(conditionHolds({ key: 'signup_enabled', field: '', in: ['false'] } as SettingFieldCondition, 'other', values)).toBe(false)
  })

  /**
   * A hide rule follows the CONFIGURED document, the same one the controls
   * show. Following the applied value instead can hide the very row that
   * repairs it: a captcha selection that is not fully configured degrades
   * at read time, so a credential field hidden until its provider is the
   * applied one could never be filled in. The hub's own schema test
   * (TestExternalProviderCredentialsAreAlwaysVisible) refuses that pairing
   * on the descriptor side; this keeps the client from re-creating it.
   */
  it('resolves depends_on against the configured value, not the applied one', () => {
    const values = new Map([
      ['captcha.selected', value('captcha.selected', '"altcha"', { valueJson: '"turnstile"', mergedJson: '"turnstile"', customized: true })],
    ])
    const condition = { key: 'captcha.selected', field: '', in: ['turnstile'] } as SettingFieldCondition
    expect(conditionHolds(condition, 'other', values)).toBe(true)
  })

  it('hiddenInSolo descriptors hide exactly in solo mode', () => {
    const rows = buildProtoRows([descriptor({ hiddenInSolo: true })], source())
    isSoloMode.mockReturnValue(false)
    expect(rows[0].descriptor.hidden?.() ?? false).toBe(false)
    isSoloMode.mockReturnValue(true)
    expect(rows[0].descriptor.hidden?.() ?? false).toBe(true)
    isSoloMode.mockReturnValue(false)
  })

  // Every captcha key declares BOTH: HiddenInSolo on the descriptor and
  // dependsOn on its fields. A chain of ternaries let the solo branch win, so
  // on a non-solo hub (where isSoloMode() is false) the dependsOn rule never
  // ran and every inactive provider's fields stayed on screen.
  it('applies dependsOn to a hiddenInSolo descriptor, not one rule or the other', () => {
    const values = new Map([['captcha.selected', value('captcha.selected', '"altcha"')]])
    const src = source(values)
    const rows = buildProtoRows([descriptor({
      key: 'captcha.turnstile',
      category: 'captcha',
      hiddenInSolo: true,
      fields: [field({
        name: 'site_key',
        label: 'Site key',
        dependsOn: { key: 'captcha.selected', field: '', in: ['turnstile'] } as SettingField['dependsOn'],
      })],
    })], src)

    isSoloMode.mockReturnValue(false)
    expect(rows[0].descriptor.hidden?.()).toBe(true)

    values.set('captcha.selected', value('captcha.selected', '"turnstile"'))
    expect(rows[0].descriptor.hidden?.()).toBe(false)

    // Solo still hides it, whatever the condition says.
    isSoloMode.mockReturnValue(true)
    expect(rows[0].descriptor.hidden?.()).toBe(true)
    isSoloMode.mockReturnValue(false)
  })

  it('carries the setting title as a keyword so a search finds every field', () => {
    const rows = buildProtoRows([descriptor({
      key: 'smtp',
      category: 'email',
      title: 'SMTP relay',
      fields: [field({ name: 'host', label: 'Host' }), field({ name: 'port', label: 'Port' })],
    })], source())
    expect(rows.map(r => r.descriptor.keywords)).toEqual([['SMTP relay'], ['SMTP relay']])
  })

  it('reads an omitted numeric object field as 0 (auto), not unset', () => {
    const src = source(new Map([['queue_budget', value('queue_budget', '{}')]]))
    const rows = buildProtoRows([descriptor({
      key: 'queue_budget',
      category: 'advanced',
      fields: [
        field({ name: 'relay_bytes', label: 'Relay', kind: SettingFieldKind.INT, unit: 'bytes' }),
        field({ name: 'worker_bytes', label: 'Worker', kind: SettingFieldKind.INT, unit: 'bytes' }),
        field({ name: 'ratio', label: 'Ratio', kind: SettingFieldKind.FLOAT }),
      ],
    })], src)
    expect(rows[0].binding.value()).toBe(0)
    expect(rows[1].binding.value()).toBe(0)
    expect(rows[2].binding.value()).toBe(0)
  })

  it('leaves an omitted non-numeric object field unset', () => {
    const src = source(new Map([['smtp', value('smtp', '{}')]]))
    const rows = buildProtoRows([descriptor({
      key: 'smtp',
      category: 'email',
      fields: [field({ name: 'host', label: 'Host', kind: SettingFieldKind.STRING })],
    })], src)
    expect(rows[0].binding.value()).toBeUndefined()
  })

  it('does not invent 0 when the setting itself is absent', () => {
    const rows = buildProtoRows([descriptor({
      key: 'queue_budget',
      category: 'advanced',
      fields: [field({ name: 'relay_bytes', kind: SettingFieldKind.INT })],
    })], source())
    expect(rows[0].binding.value()).toBeUndefined()
  })

  /**
   * The control EDITS, so it binds to the CONFIGURED document — the stored
   * row merged onto the code default. The applied value is reported beside
   * it, never inside it.
   *
   * Binding the control to the effective document instead made the note a
   * tautology: it printed back the value the control already showed, and
   * the configured value appeared nowhere at all. The admin then edited a
   * figure nobody stored — a nudge on an auto-sized queue budget would
   * freeze the auto-sized number as a literal.
   */
  it('binds the control to the configured value, not the applied one', () => {
    // Dev mode holds sign-up open while no row is stored, so the configured
    // value is the code default that an edit would replace.
    const scalar = new Map([['signup_enabled', value('signup_enabled', 'true', { mergedJson: 'false' })]])
    const [signup] = buildProtoRows([descriptor({
      key: 'signup_enabled',
      fields: [field({ kind: SettingFieldKind.BOOL })],
    })], source(scalar))
    expect(signup.binding.value()).toBe(false)
    expect(signup.binding.effective?.()).toBe(true)

    // The sibling shape, per field: a stored queue budget of 0 auto-sizes
    // from the process memory limit.
    const object = new Map([['queue_budget', value('queue_budget', '{"relay_bytes":268435456}', {
      valueJson: '{"relay_bytes":0}',
      mergedJson: '{"relay_bytes":0}',
      customized: true,
    })]])
    const [relay] = buildProtoRows([descriptor({
      key: 'queue_budget',
      category: 'advanced',
      fields: [field({ name: 'relay_bytes', label: 'Relay', kind: SettingFieldKind.INT })],
    })], source(object))
    expect(relay.binding.value()).toBe(0)
    expect(relay.binding.effective?.()).toBe(268435456)
  })

  /**
   * The "currently in effect" note reports that a READ-TIME RULE replaced
   * the configured value, so its left operand is the MERGED document — the
   * stored row on top of the code default — never the stored row alone.
   *
   * The distinction is invisible on a customized scalar, where the stored
   * value and the merged one are the same document. It decides every other
   * row: an uncustomized key and an object field the operator never touched
   * both have NO stored value, so the stored document differs from the
   * effective one on each of them and the note printed the plain default as
   * if the hub had overridden something.
   */
  it('prints no effect note on a row that no read-time rule overrides', () => {
    const values = new Map([
      // Nothing stored: the code default is both merged and effective.
      ['signup_enabled', value('signup_enabled', 'true', { mergedJson: 'true' })],
      // The operator stored `port` alone. `host` comes from the code
      // default, which is exactly what merged_json carries.
      ['smtp', value('smtp', '{"host":"mail.example.com","port":2525}', {
        valueJson: '{"port":2525}',
        mergedJson: '{"host":"mail.example.com","port":2525}',
        customized: true,
      })],
    ])
    const src = source(values)

    const [signup] = buildProtoRows([descriptor({
      key: 'signup_enabled',
      fields: [field({ kind: SettingFieldKind.BOOL })],
    })], src)
    expect(signup.binding.effective?.()).toBeUndefined()

    const smtpRows = buildProtoRows([descriptor({
      key: 'smtp',
      category: 'email',
      fields: [
        field({ name: 'host', label: 'Host' }),
        field({ name: 'port', label: 'Port', kind: SettingFieldKind.INT }),
      ],
    })], src)
    expect(smtpRows.map(r => r.descriptor.id)).toEqual(['smtp.host', 'smtp.port'])
    for (const row of smtpRows)
      expect(row.binding.effective?.(), `${row.descriptor.id} prints an effect note`).toBeUndefined()
  })

  it('reports the applied value when a read-time rule overrides the merged one', () => {
    // Dev mode forces signup on although the stored row disables it.
    const scalar = new Map([['signup_enabled', value('signup_enabled', 'true', {
      valueJson: 'false',
      mergedJson: 'false',
      customized: true,
    })]])
    const [row] = buildProtoRows([descriptor({
      key: 'signup_enabled',
      fields: [field({ kind: SettingFieldKind.BOOL })],
    })], source(scalar))
    expect(row.binding.effective?.()).toBe(true)

    // Per field, and on a field the stored row never listed: email
    // verification defaults to on, and the hub drops it while no SMTP relay
    // is configured. The sibling the rule leaves alone keeps its silence.
    const object = new Map([['signup', value('signup', '{"enabled":true,"verify_email":false}', {
      valueJson: '{"enabled":true}',
      mergedJson: '{"enabled":true,"verify_email":true}',
      customized: true,
    })]])
    const rows = buildProtoRows([descriptor({
      key: 'signup',
      category: 'signup',
      fields: [
        field({ name: 'enabled', label: 'Enabled', kind: SettingFieldKind.BOOL }),
        field({ name: 'verify_email', label: 'Verify email', kind: SettingFieldKind.BOOL }),
      ],
    })], source(object))
    const byId = new Map(rows.map(r => [r.descriptor.id, r]))
    expect(byId.get('signup.enabled')!.binding.effective?.()).toBeUndefined()
    expect(byId.get('signup.verify_email')!.binding.effective?.()).toBe(false)
  })

  // The ACCOUNT scope sends no merged document, and it has no read-time
  // override — its effective value already IS its merged value. Without the
  // fallback the comparison reads an empty document, every account row
  // differs from it, and each one prints a note that never applies there.
  it('falls back to the effective document when the scope sends no merged one', () => {
    const values = new Map([['font_size', value('font_size', '14', { valueJson: '13', customized: true })]])
    const [row] = buildProtoRows([descriptor({
      key: 'font_size',
      category: 'appearance',
      fields: [field({ kind: SettingFieldKind.INT })],
    })], source(values))
    expect(row.binding.value()).toBe(14)
    expect(row.binding.effective?.()).toBeUndefined()
  })

  it('exposes customized/reset against the key and skips unknown categories', async () => {
    const src = source(new Map([['signup_enabled', value('signup_enabled', 'true', { customized: true })]]))
    const rows = buildProtoRows([
      descriptor({ key: 'signup_enabled' }),
      descriptor({ key: 'weird', category: 'not-a-category' }),
    ], src)
    expect(rows).toHaveLength(1)
    expect(rows[0].binding.customized?.()).toBe(true)
    await rows[0].binding.reset!()
    expect(src.reset).toHaveBeenCalledWith('signup_enabled')
  })

  // `CATEGORY_IDS` is DERIVED from NAV_GROUPS, so asserting that every
  // group's category is in the set proves nothing. The direction that
  // protects the dialog is the reverse: a CategoryId with no nav group
  // makes `buildProtoRows` skip the descriptor, and the row disappears
  // with no error anywhere.
  //
  // The record is exhaustive over the union, so a new CategoryId fails
  // `tsc` here until it is listed, and then fails this assertion until it
  // has a nav group.
  it('gives every CategoryId a nav group', () => {
    const declared: Record<CategoryId, true> = {
      'appearance': true,
      'apps': true,
      'notifications': true,
      'chat': true,
      'terminal': true,
      'files': true,
      'shortcuts': true,
      'advanced': true,
      'account': true,
      'general': true,
      'signup': true,
      'email': true,
      'captcha': true,
      'rate-limits': true,
      'limits': true,
    }
    for (const id of Object.keys(declared) as CategoryId[])
      expect(CATEGORY_IDS.has(id), `category "${id}" has no nav group`).toBe(true)
    // And no nav group points at a category the union dropped.
    expect(CATEGORY_IDS.size).toBe(Object.keys(declared).length)
  })

  // A row's category must resolve to a group on ITS OWN side of the
  // admin split. `PreferencesDialog` searches with `g.admin === admin`,
  // so a browser row in an admin-only category is unreachable from
  // search even though the panel renders it.
  it('gives every browser descriptor a non-admin nav group', () => {
    const nonAdmin = new Set(NAV_GROUPS.filter(g => !g.admin).map(g => g.category))
    const rows = createBrowserRows(makeFakePrefs() as unknown as PreferencesState, accountWireDescriptors())
    expect(rows.length).toBe(browserSettings.length)
    for (const { descriptor: d } of rows)
      expect(nonAdmin.has(d.category), `browser row "${d.id}" sits in admin-only "${d.category}"`).toBe(true)
  })
})

describe('custom editors', () => {
  // The browser-tier ids are typed `CustomEditorId` and CUSTOM_EDITORS is a
  // `Record<CustomEditorId, …>`, so walking them proves only what `tsc`
  // already proves. The untyped path is the wire one: `customId` arrives as
  // a plain string from the hub.
  it('resolves every declared editor id through the wire path', () => {
    expect(CUSTOM_EDITOR_IDS.length).toBeGreaterThan(0)
    for (const id of CUSTOM_EDITOR_IDS) {
      const control = controlForField(field({ kind: SettingFieldKind.CUSTOM, customId: id }))
      expect(control, `customId "${id}" produced no control`).toEqual({ kind: 'custom', id })
      expect(CUSTOM_EDITORS[id], `customId "${id}" has no component`).toBeTypeOf('function')
    }
  })

  // A newer hub can declare an editor this client does not carry. There is
  // no honest fallback for an opaque value, so the field is dropped — a
  // cast to CustomEditorId instead produced a row with a label, help text,
  // and no control at all.
  it('drops a field whose custom editor this client does not carry', () => {
    expect(controlForField(field({ kind: SettingFieldKind.CUSTOM, customId: 'from-a-newer-hub' })))
      .toBeUndefined()

    const rows = buildProtoRows([descriptor({
      key: 'k',
      fields: [
        field({ name: 'known', kind: SettingFieldKind.CUSTOM, customId: 'keybindings' }),
        field({ name: 'alien', kind: SettingFieldKind.CUSTOM, customId: 'from-a-newer-hub' }),
      ],
    })], source())
    expect(rows.map(r => r.descriptor.id)).toEqual(['k.known'])
  })

  /**
   * WHICH editor each account row opens, not merely that it opens one.
   *
   * The two assertions above read `typeof … === 'function'` and a
   * descriptor id, and every component satisfies both — so mapping any
   * account id at any other editor passes them unchanged. The Account group
   * is six rows over six editors now, and transposing two of them is the
   * mistake identity catches.
   *
   * This file renders nothing, so it pins the mapping only. What each editor
   * puts on screen is tested beside that editor.
   */
  it('opens the matching editor for each account row', () => {
    expect(CUSTOM_EDITORS.accountProfile).toBe(AccountProfile)
    expect(CUSTOM_EDITORS.accountEmail).toBe(AccountEmail)
    expect(CUSTOM_EDITORS.accountPasskeys).toBe(AccountPasskeys)
  })

  // Every browser-tier custom row still needs its component; the type only
  // constrains the table's KEYS, not that a rendered id is in the table.
  // The account rows' custom ids arrive on the WIRE (`keybindings` is a
  // `customId` string), so this reads the rendered rows.
  it('gives every browser custom row a component', () => {
    const ids = createBrowserRows(makeFakePrefs() as unknown as PreferencesState, accountWireDescriptors())
      .flatMap(({ descriptor }) => descriptor.control.kind === 'custom' ? [descriptor.control.id] : [])
    expect(ids.length).toBeGreaterThan(0)
    for (const id of ids)
      expect(CUSTOM_EDITORS[id], `browser row renders "${id}" with no component`).toBeTypeOf('function')
  })
})

describe('buildProtoRows per-field customized and reset', () => {
  const smtp = descriptor({
    key: 'smtp',
    fields: [
      field({ name: 'host', label: 'Host' }),
      field({ name: 'port', label: 'Port', kind: SettingFieldKind.INT }),
      field({ name: 'password', label: 'Password', secret: true }),
    ],
  })

  it('marks only the fields the stored document lists', () => {
    const values = new Map([['smtp', value(
      'smtp',
      '{"host":"mail.example.com","port":587}',
      { valueJson: '{"port":2525}', customized: true, secretSet: { password: true } },
    )]])
    const rows = buildProtoRows([smtp], source(values))

    const byId = new Map(rows.map(r => [r.descriptor.id, r]))
    expect(byId.get('smtp.port')!.binding.customized!()).toBe(true)
    // The key-level flag used to mark every field, so an untouched host
    // offered a Reset that destroyed the whole row.
    expect(byId.get('smtp.host')!.binding.customized!()).toBe(false)
    // A secret has no readable stored document; its own secretSet entry
    // is the answer.
    expect(byId.get('smtp.password')!.binding.customized!()).toBe(true)
  })

  it('declares that a field row reset clears the whole key', () => {
    const rows = buildProtoRows([smtp], source())
    for (const row of rows)
      expect(row.binding.resetsWholeKey).toBe('smtp')
  })

  it('leaves resetsWholeKey unset on a scalar row, whose reset is exact', () => {
    const scalar = descriptor({ key: 'public_url', fields: [field({ name: '' })] })
    const [row] = buildProtoRows([scalar], source())
    expect(row.binding.resetsWholeKey).toBeUndefined()
  })
})

describe('controlForField secret handling', () => {
  it('renders a secret field as a secret control whatever its kind', () => {
    // The ALTCHA signing key is declared BYTES + secret. Testing
    // field.secret inside the STRING case alone rendered it as a text box
    // showing the literal "<redacted>" placeholder.
    const bytesSecret = field({ name: 'hmac_key', kind: SettingFieldKind.BYTES, secret: true })
    const control = mustControl(bytesSecret, () => value('captcha.altcha', '{}', { secretSet: { hmac_key: true } }))
    expect(control.kind).toBe('secret')
    expect(control.kind === 'secret' && control.isSet()).toBe(true)

    for (const kind of [SettingFieldKind.STRING, SettingFieldKind.BYTES, SettingFieldKind.STRING_LIST]) {
      expect(mustControl(field({ name: 's', kind, secret: true })).kind).toBe('secret')
    }
  })

  it('still renders a non-secret BYTES field as text so an unknown kind degrades', () => {
    expect(mustControl(field({ kind: SettingFieldKind.BYTES })).kind).toBe('text')
  })
})
