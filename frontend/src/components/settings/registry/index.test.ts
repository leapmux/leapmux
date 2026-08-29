import type { SettingBinding, SettingDescriptor } from '../types'
import type { PreferencesState } from '~/context/PreferencesContext'
import type { SettingDescriptor as ProtoSettingDescriptor } from '~/generated/leapmux/v1/settings_pb'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { accountWireDescriptors, goldenAccountSchema } from '~/test-support/accountSchema'
import { makeFakePrefs } from '~/test-support/preferencesFake'
import { CATEGORY_IDS } from '../protoRegistry'
import { CUSTOM_EDITOR_OWNS_ITS_VALUE } from './bindings'
import { CLAIMED_PROTO_KEYS, createBrowserRows } from './index'
import { browserSettings, buildBrowserReset } from './settings'

// Every account-setting write the bindings can issue goes to the mocked
// context below; none of these tests touch a hub.
vi.mock('~/api/clients', () => ({
  userClient: {},
  authClient: {},
}))

// Solo mode is the one environment fact a hide rule reads. Control it per
// test rather than depending on the fabricated default.
const solo = vi.hoisted(() => vi.fn(() => false))
vi.mock('~/lib/systemInfo', async importOriginal => ({
  ...(await importOriginal<typeof import('~/lib/systemInfo')>()),
  isSoloMode: () => solo(),
}))

beforeEach(() => {
  solo.mockReturnValue(false)
})

// The registry is HALF of every account row: the hub's descriptor states
// the shape and this table states the presentation. Passing the descriptors
// the hub really sends is therefore what makes these tests exercise the row
// a user sees, rather than one the test invented.
const wire = accountWireDescriptors()

/** The rendered descriptor of every registry row, in declaration order. */
function descriptorsOf(
  prefs: PreferencesState,
  descriptors: readonly ProtoSettingDescriptor[] = wire,
): SettingDescriptor[] {
  return createBrowserRows(prefs, descriptors).map(row => row.descriptor)
}

/** Every registry row's binding, addressed by row id. */
function bindingsOf(
  prefs: PreferencesState,
  descriptors: readonly ProtoSettingDescriptor[] = wire,
): Record<string, SettingBinding> {
  return Object.fromEntries(
    createBrowserRows(prefs, descriptors).map(row => [row.descriptor.id, row.binding]),
  )
}

/** One rendered row's control, by row id. */
function controlOf(id: string) {
  return descriptorsOf(makeFakePrefs() as unknown as PreferencesState).find(d => d.id === id)?.control
}

describe('browserSettings registry', () => {
  it('has unique ids', () => {
    const ids = browserSettings.map(d => d.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('places every rendered row in a category the navigation knows', () => {
    const descriptors = descriptorsOf(makeFakePrefs() as unknown as PreferencesState)
    expect(descriptors.length).toBe(browserSettings.length)
    for (const d of descriptors)
      expect(CATEGORY_IDS.has(d.category), `row "${d.id}" sits in "${d.category}"`).toBe(true)
  })

  it('declares a sentinel shape on every entry', () => {
    expect(browserSettings.length).toBeGreaterThan(0)
    for (const decl of browserSettings)
      expect(['nullable', 'default-on', 'default-off']).toContain(decl.sentinel)
  })

  // A row whose CUSTOM EDITOR owns its value has no scalar to bind, and its
  // `value`/`set` pair is never called. Seven rows are in that shape, and each
  // used to re-type the same two no-op closures.
  //
  // The assertion is IDENTITY, not behavior: two hand-written no-op literals
  // behave alike, so a test that called `value()` and compared to null would
  // pass for a re-typed copy and catch nothing. Sharing the one constant is
  // what this pins, so the ninth such row reuses it instead of inventing a
  // tenth spelling.
  it('binds every custom-editor row to the one shared no-op binding', () => {
    const prefs = makeFakePrefs() as unknown as PreferencesState
    const customRows = browserSettings.filter(
      decl => decl.protoKey === undefined && decl.control.kind === 'custom',
    )
    expect(customRows.length).toBe(8)
    for (const decl of customRows)
      expect(decl.bind(prefs), `row "${decl.id}"`).toBe(CUSTOM_EDITOR_OWNS_ITS_VALUE)
  })

  // WHICH account-scoped rows demand a proven factor, stated as a set.
  //
  // The asymmetry between the two app rows is the decision worth pinning, and
  // it runs in both directions:
  //
  // - `apps.registrations` DOES, because editing a registration rewrites where
  //   a consent redirects — it diverts an authorization code already in flight
  //   to an address the editor chose, which is the most dangerous write in the
  //   feature. The Hub enforces the same rule on every AppService write verb.
  // - `account.connectedApps` does NOT, because disconnecting only ever
  //   reduces access, and demanding a password from somebody who has just
  //   realized an app is malicious is the wrong failure mode.
  //
  // A `hub`-scoped row needs no flag: descriptorNeedsElevation answers true for
  // the whole scope, which is why the set below is drawn from account rows only.
  it('demands a proven factor on exactly the account rows that create authority', () => {
    // Narrowed on `protoKey === undefined`, which is the union's discriminant
    // and the reason only these can carry the flag at all: an account-BACKED
    // entry states none of the descriptor's shape, so the hub's own descriptor
    // would have to carry it. Every row below is a custom account editor, which
    // is a browser-only entry.
    const elevated = browserSettings
      .filter(decl => decl.protoKey === undefined && decl.needsElevation === true)
      .map(decl => decl.id)
      .sort()

    expect(elevated).toEqual([
      'account.email',
      'account.linkedProviders',
      'account.passkeys',
      'account.password',
      'apps.registrations',
    ])
    // Stated separately, because reading it off the list above only says it is
    // absent — not that its absence is the decision rather than an omission.
    const connectedApps = browserSettings.find(d => d.id === 'account.connectedApps')
    expect(connectedApps?.protoKey).toBeUndefined()
    expect(connectedApps && connectedApps.protoKey === undefined && connectedApps.needsElevation)
      .toBeFalsy()
  })

  it('derives its descriptors from the same entries (same ids, no duplicates)', () => {
    const descriptors = descriptorsOf(makeFakePrefs() as unknown as PreferencesState)
    expect(descriptors.map(d => d.id)).toEqual(browserSettings.map(d => d.id))
  })

  // `addLabel` is the accessible name of BOTH the add text box and the add
  // button of a string-list row. Two rows that share it put two identically
  // named fields and two identically named buttons in one panel, and a
  // screen-reader user cannot tell the UI stack from the monospace one.
  //
  // `controlForField` cannot name them apart -- it answers for every key of
  // both scopes at once, so it builds a plain "Add" -- which is exactly why
  // the declaration carries the name and this test reads the RENDERED
  // control rather than the declaration it came from.
  it('names every add affordance apart', () => {
    const stringLists = descriptorsOf(makeFakePrefs() as unknown as PreferencesState)
      .map(d => d.control)
      .filter(c => c.kind === 'stringList')
    expect(stringLists.length).toBeGreaterThan(1)
    const addLabels = stringLists.map(c => c.addLabel)
    expect(new Set(addLabels).size).toBe(addLabels.length)
    expect(addLabels).not.toContain('Add')

    const uiStack = controlOf('appearance.uiFontStack')
    const monoStack = controlOf('appearance.monoFontStack')
    expect(uiStack).toEqual({ kind: 'stringList', addLabel: 'Add UI font' })
    expect(monoStack).toEqual({ kind: 'stringList', addLabel: 'Add monospace font' })
  })

  it('claims object-shaped proto keys by the setting key, not by field id', () => {
    expect(CLAIMED_PROTO_KEYS.has('ui_fonts')).toBe(true)
    expect(CLAIMED_PROTO_KEYS.has('mono_fonts')).toBe(true)
    expect(CLAIMED_PROTO_KEYS.has('ui_fonts.enabled')).toBe(false)
    expect(CLAIMED_PROTO_KEYS.has('mono_fonts.fonts')).toBe(false)
  })
})

// The hub declares each account key's shape, and this registry joins its
// own presentation onto that reply. So an account row cannot exist before
// the reply does -- the honest alternative was inventing a control kind, an
// option list and a pair of bounds, and storing whatever a user then picked
// through them.
describe('createBrowserRows without a loaded account schema', () => {
  const prefs = () => makeFakePrefs() as unknown as PreferencesState

  it('renders the browser-only rows and no account row', () => {
    const ids = descriptorsOf(prefs(), []).map(d => d.id)
    expect(ids).toContain('chat.expandAgentThoughts')
    expect(ids).toContain('advanced.resetBrowserOverrides')
    expect(ids).not.toContain('appearance.theme')
    expect(ids).not.toContain('appearance.uiFontStack')
    expect(ids).not.toContain('shortcuts.keybindings')
    expect(ids).not.toContain('advanced.debugLogging')
  })

  it('brings every account row back once the schema arrives', () => {
    expect(descriptorsOf(prefs(), wire).map(d => d.id))
      .toEqual(browserSettings.map(d => d.id))
  })

  it('drops only the entries whose key the hub stopped declaring', () => {
    const withoutFonts = wire.filter(d => d.key !== 'ui_fonts')
    const ids = descriptorsOf(prefs(), withoutFonts).map(d => d.id)
    expect(ids).not.toContain('appearance.uiFonts')
    expect(ids).not.toContain('appearance.uiFontStack')
    expect(ids).toContain('appearance.monoFonts')
    expect(ids).toContain('appearance.theme')
  })

  // The join is per FIELD, not per key: an object-shaped key that loses one
  // of its two fields must lose exactly that row.
  it('drops one row when the hub stops declaring one field of a key', () => {
    const withoutStack = wire.map(d => (d.key === 'mono_fonts'
      ? { ...d, fields: d.fields.filter(f => f.name !== 'fonts') }
      : d)) as ProtoSettingDescriptor[]
    const ids = descriptorsOf(prefs(), withoutStack).map(d => d.id)
    expect(ids).toContain('appearance.monoFonts')
    expect(ids).not.toContain('appearance.monoFontStack')
  })

  // A category with no navigation group has nowhere to render, which is the
  // same answer `buildProtoRows` gives a hub row.
  it('drops a row the hub moved to a category the navigation does not know', () => {
    const moved = wire.map(d => (d.key === 'theme'
      ? { ...d, category: 'from-a-newer-hub' }
      : d)) as ProtoSettingDescriptor[]
    expect(descriptorsOf(prefs(), moved).map(d => d.id)).not.toContain('appearance.theme')
  })

  // The wire, not the declaration, decides where a row sits.
  it('follows the hub when it moves a key to another known category', () => {
    const moved = wire.map(d => (d.key === 'theme'
      ? { ...d, category: 'advanced' }
      : d)) as ProtoSettingDescriptor[]
    const theme = descriptorsOf(prefs(), moved).find(d => d.id === 'appearance.theme')
    expect(theme?.category).toBe('advanced')
  })

  // `restart` and `hiddenInSolo` are the descriptor's own facts, and both
  // reach the row. A hide rule is a REASON, never a replacement: the
  // declaration's own rules still apply beside the hub's.
  it('carries the hub restart flag and its solo hide rule onto the row', () => {
    const marked = wire.map(d => (d.key === 'turn_end_sound_volume'
      ? { ...d, restart: true, hiddenInSolo: true }
      : d)) as ProtoSettingDescriptor[]
    const row = () => descriptorsOf(prefs(), marked).find(d => d.id === 'notifications.turnEndSoundVolume')
    expect(row()?.restart).toBe(true)
    expect(row()?.hidden?.()).toBe(false)

    solo.mockReturnValue(true)
    expect(row()?.hidden?.()).toBe(true)
  })

  // The declaration's own rule is not lost when the hub adds one of its
  // own: the volume row hides while NEITHER tier can play a sound, and that
  // still holds outside solo mode.
  it('keeps the declared hide rule beside the hub one', () => {
    const marked = wire.map(d => (d.key === 'turn_end_sound_volume'
      ? { ...d, hiddenInSolo: true }
      : d)) as ProtoSettingDescriptor[]
    const base = makeFakePrefs()
    const silent = {
      ...base,
      dual: {
        ...base.dual,
        turnEndSound: { ...base.dual.turnEndSound, resolved: () => 'none', account: () => 'none' },
      },
    } as unknown as PreferencesState
    const row = descriptorsOf(silent, marked).find(d => d.id === 'notifications.turnEndSoundVolume')
    expect(row?.hidden?.()).toBe(true)
  })
})

describe('createBrowserRows bindings', () => {
  /** A theme preference whose browser tier is a live signal. */
  function themePrefs(customized: boolean) {
    const [browserTheme, setBrowserThemeSignal] = createSignal<'dark' | 'light' | 'system' | null>(null)
    const setBrowserTheme = vi.fn((v: 'dark' | 'light' | 'system' | null) => setBrowserThemeSignal(v))
    const setAccountTheme = vi.fn()
    const resetTheme = vi.fn(async () => {})
    const base = makeFakePrefs()
    const prefs = {
      ...base,
      dual: {
        ...base.dual,
        theme: {
          protoKey: 'theme',
          resolved: () => browserTheme() ?? 'system',
          browser: browserTheme,
          setBrowser: setBrowserTheme,
          account: () => 'system',
          setAccount: setAccountTheme,
          customized: () => customized,
          reset: resetTheme,
        },
      },
    } as unknown as PreferencesState
    return { prefs, setBrowserTheme, setAccountTheme, resetTheme }
  }

  it('dual bindings expose overridden/clearOverride and edit the selected tier', () => {
    const { prefs, setBrowserTheme, setAccountTheme, resetTheme } = themePrefs(true)
    const bindings = bindingsOf(prefs)
    const theme = bindings['appearance.theme']
    expect(theme.overridden).toBeDefined()
    expect(theme.clearOverride).toBeDefined()

    // Account tier (no override): the control edits the account value, and
    // the row offers the account tier's Reset.
    expect(theme.overridden!()).toBe(false)
    expect(theme.value()).toBe('system')
    expect(theme.customized!()).toBe(true)
    theme.set('dark')
    expect(setAccountTheme).toHaveBeenCalledWith('dark')
    void theme.reset!()
    expect(resetTheme).toHaveBeenCalledTimes(1)

    // Switching to the browser tier seeds the override with the current value.
    theme.beginOverride!()
    expect(setBrowserTheme).toHaveBeenCalledWith('system')

    // Override tier: the control edits the browser value.
    expect(theme.overridden!()).toBe(true)
    theme.set('dark')
    expect(setBrowserTheme).toHaveBeenLastCalledWith('dark')

    theme.clearOverride!()
    expect(setBrowserTheme).toHaveBeenLastCalledWith(null)
  })

  // A dual row EDITING its browser tier must not offer the account tier's
  // Reset. The resolved value is `browser() ?? account()`, so removing the
  // account default leaves the control exactly where it stands: the user
  // destroys their stored default and sees nothing move.
  it('hides the account Reset while a dual row edits its browser tier', () => {
    const { prefs } = themePrefs(true)
    const theme = bindingsOf(prefs)['appearance.theme']
    expect(theme.customized!()).toBe(true)

    theme.beginOverride!()
    expect(theme.overridden!()).toBe(true)
    expect(theme.customized!()).toBe(false)

    theme.clearOverride!()
    expect(theme.customized!()).toBe(true)
  })

  it('reports no customization for a key the account tier does not store', () => {
    const { prefs } = themePrefs(false)
    expect(bindingsOf(prefs)['appearance.theme'].customized!()).toBe(false)
  })

  // The reset RPC deletes the WHOLE `{enabled, fonts}` document -- there is
  // no per-field reset on the wire -- so a font row's Reset takes the other
  // row's value with it. Naming the key is what makes SettingRow ask first
  // instead of rendering a plain button.
  it('marks both halves of a font tier as resetting the whole key', () => {
    const bindings = bindingsOf(makeFakePrefs() as unknown as PreferencesState)
    expect(bindings['appearance.uiFonts'].resetsWholeKey).toBe('ui_fonts')
    expect(bindings['appearance.uiFontStack'].resetsWholeKey).toBe('ui_fonts')
    expect(bindings['appearance.monoFonts'].resetsWholeKey).toBe('mono_fonts')
    expect(bindings['appearance.monoFontStack'].resetsWholeKey).toBe('mono_fonts')
  })

  // A scalar dual row IS its key, so its Reset removes exactly what the row
  // shows and the plain button is exact.
  it('leaves a scalar dual row with a plain Reset', () => {
    const bindings = bindingsOf(makeFakePrefs() as unknown as PreferencesState)
    expect(bindings['appearance.theme'].resetsWholeKey).toBeUndefined()
    expect(bindings['advanced.debugLogging'].resetsWholeKey).toBeUndefined()
  })

  it('font stack rows write whole-object tiers', () => {
    const setAccountMonoFonts = vi.fn()
    const setBrowserMonoFont = vi.fn()
    const monoTier = (browser: () => { enabled: boolean, fonts: string[] } | null) => ({
      resolved: () => browser() ?? { enabled: true, fonts: ['A'] },
      browser,
      setBrowser: setBrowserMonoFont,
      account: () => ({ enabled: true, fonts: ['A'] }),
      setAccount: setAccountMonoFonts,
    })
    const base = makeFakePrefs()
    const prefs = {
      ...base,
      dual: { ...base.dual, monoFonts: monoTier(() => null) },
      accountCustomized: () => ({}),
      resetUserSetting: vi.fn(async () => true),
    } as unknown as PreferencesState

    const bindings = bindingsOf(prefs)
    bindings['appearance.monoFontStack'].set(['B', 'C'])
    expect(setAccountMonoFonts).toHaveBeenCalledWith({ enabled: true, fonts: ['B', 'C'] })
    bindings['appearance.monoFonts'].set(false)
    expect(setAccountMonoFonts).toHaveBeenLastCalledWith({ enabled: false, fonts: ['A'] })

    // Override tier merges into the whole browser object, never half of it.
    const overridden = {
      ...prefs,
      dual: { ...base.dual, monoFonts: monoTier(() => ({ enabled: true, fonts: ['A'] })) },
    } as unknown as PreferencesState
    const over = bindingsOf(overridden)
    over['appearance.monoFontStack'].set(['Z'])
    expect(setBrowserMonoFont).toHaveBeenCalledWith({ enabled: true, fonts: ['Z'] })
  })
})

describe('buildBrowserReset', () => {
  let fake: ReturnType<typeof makeFakePrefs>

  beforeEach(() => {
    fake = makeFakePrefs()
  })

  it('clears nullable tiers to null / undefined (delete the stored key)', () => {
    for (const action of buildBrowserReset(fake as unknown as PreferencesState))
      action.reset()
    expect(fake.dual.theme.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.dual.terminalTheme.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.dual.diffView.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.dual.turnEndSound.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.dual.turnEndSoundVolume.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.dual.debugLogging.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.dual.uiFonts.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.dual.monoFonts.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.setEnterKeyMode).toHaveBeenCalledWith(null)
    expect(fake.setTerminalRenderer).toHaveBeenCalledWith(null)
    expect(fake.setPreferredEditorId).toHaveBeenCalledWith(undefined)
  })

  it('resets default-on tiers to true (absent means true, never a stored `true`)', () => {
    for (const action of buildBrowserReset(fake as unknown as PreferencesState))
      action.reset()
    expect(fake.setExpandAgentThoughts).toHaveBeenCalledWith(true)
    expect(fake.setRevealAfterDownload).toHaveBeenCalledWith(true)
    expect(fake.setShowComposerStatusBar).toHaveBeenCalledWith(true)
    expect(fake.setDirectoryPickerShowHidden).toHaveBeenCalledWith(true)
  })

  it('resets default-off tiers to false (delete the opt-in key)', () => {
    for (const action of buildBrowserReset(fake as unknown as PreferencesState))
      action.reset()
    expect(fake.setTerminalOsNotifications).toHaveBeenCalledWith(false)
    expect(fake.setShowHiddenMessages).toHaveBeenCalledWith(false)
  })

  it('leaves trust state (key pins) out of the reset list', () => {
    const ids = buildBrowserReset(fake as unknown as PreferencesState).map(a => a.id)
    expect(ids).not.toContain('advanced.keyPins')
    expect(ids).not.toContain('advanced.resetBrowserOverrides')
    expect(ids).not.toContain('shortcuts.keybindings')
    expect(ids).not.toContain('account.profile')
  })

  // A dual entry's browser reset IS the `clearOverride` its own binding
  // already builds. Two copies of that had drifted into a pair that
  // cleared one font tier twice and could clear the other never.
  it('emits one action per dual KEY, not one per row', () => {
    const actions = buildBrowserReset(fake as unknown as PreferencesState)
    const ids = actions.map(a => a.id)
    expect(ids).toContain('appearance.uiFonts')
    expect(ids).not.toContain('appearance.uiFontStack')
    expect(ids).toContain('appearance.monoFonts')
    expect(ids).not.toContain('appearance.monoFontStack')

    for (const action of actions)
      action.reset()
    expect(fake.dual.uiFonts.setBrowser).toHaveBeenCalledTimes(1)
    expect(fake.dual.monoFonts.setBrowser).toHaveBeenCalledTimes(1)
  })

  // Every reset is otherwise a full read, parse, serialize and write of the
  // whole preferences document, and one `storage` event per field.
  it('runs the whole reset inside one browser-preferences batch', () => {
    const decl = browserSettings.find(d => d.id === 'advanced.resetBrowserOverrides')
    decl!.bind(fake as unknown as PreferencesState).set(null)
    expect(fake.batchBrowserPrefWrites).toHaveBeenCalledTimes(1)
    expect(fake.dual.theme.setBrowser).toHaveBeenCalledWith(null)
    expect(fake.setExpandAgentThoughts).toHaveBeenCalledWith(true)
  })

  it('marks Reset overrides as a danger action (ConfirmButton chrome)', () => {
    expect(controlOf('advanced.resetBrowserOverrides'))
      .toEqual({ kind: 'action', label: 'Reset overrides', danger: true })
  })
})

// The nine account settings used to be declared TWICE -- in Go
// (backend/internal/hub/usersettings/keys.go) and again here -- and the two
// copies had already drifted ("Side by side" against "Side-by-Side", "Ding
// dong" against "Ding Dong"). The registry no longer states any of the
// shape: the hub's descriptor supplies the category, the control kind, the
// enum values and the bounds, and this table supplies only the text a user
// reads plus the device tier the wire cannot express.
//
// What is left to check is therefore the JOIN, and it is what a wrong join
// costs that these cases pin. Both sides read one golden file: a key, field
// or enum value that moves in Go rewrites it through
// TestAccountSchemaMatchesGolden, and these cases fail until the
// declarations catch up.
describe('account schema parity with the Go declarations', () => {
  const golden = goldenAccountSchema()

  /** The registry entry that edits one proto key and field, if any. */
  function entryFor(protoKey: string, fieldName: string) {
    return browserSettings.find(d =>
      d.protoKey === protoKey && (d.protoField ?? '') === fieldName)
  }

  /** The rendered row for one proto key and field, if any. */
  function rowFor(protoKey: string, fieldName: string) {
    const id = entryFor(protoKey, fieldName)?.id
    if (id === undefined)
      return undefined
    return descriptorsOf(makeFakePrefs() as unknown as PreferencesState).find(d => d.id === id)
  }

  it('claims exactly the keys the hub declares', () => {
    expect([...CLAIMED_PROTO_KEYS].sort()).toEqual(golden.map(k => k.key).sort())
  })

  // A key can match while a FIELD does not, and an unmatched field renders
  // no row at all: the join is on key AND field, so a `protoField` naming a
  // field the hub dropped loses that row in silence.
  it('claims exactly the fields the hub declares', () => {
    const declared = golden.flatMap(k => k.fields.map(f => `${k.key}/${f.name}`)).sort()
    const claimed = browserSettings
      .flatMap(d => (d.protoKey === undefined ? [] : [`${d.protoKey}/${d.protoField ?? ''}`]))
      .sort()
    expect(claimed).toEqual(declared)
  })

  // The registry no longer states which values exist -- the wire does -- so
  // what it can still get wrong is failing to NAME one. An unnamed value
  // falls back to its own slug, which is legible but is not English, and
  // this is what catches a value Go added before the dialog names it.
  it('names every enum value the hub declares', () => {
    let checked = 0
    for (const key of golden) {
      for (const field of key.fields) {
        if (!field.enumValues?.length)
          continue
        const control = rowFor(key.key, field.name)?.control
        expect(control?.kind, `${key.key}.${field.name} control kind`).toBe('enum')
        if (control?.kind !== 'enum')
          continue
        // Same values, same order: the row offers exactly what the hub's
        // `validateEnum` accepts, because it reads them off the reply.
        expect(control.options.map(o => o.value)).toEqual(field.enumValues.map(o => o.value))
        for (const option of control.options) {
          expect(
            option.label,
            `${key.key}.${field.name} value "${option.value}" has no declared name`,
          ).not.toBe(option.value)
          expect(option.label).not.toBe('')
          checked++
        }
      }
    }
    expect(checked, 'the parity check must actually compare some enum names').toBeGreaterThan(0)
  })

  // The KIND is the join's other half: a control that edits a string where
  // the hub stores an int sends a document the typed decode refuses. It is
  // now derived rather than declared, so this pins the DERIVATION -- and it
  // is the case that fails when Go adds a field kind this client has no
  // control for.
  it('renders a control whose shape matches the declared field kind', () => {
    const kindsFor: Record<string, string[]> = {
      'bool': ['toggle'],
      'integer': ['number', 'slider'],
      'float': ['number', 'slider'],
      'string': ['text', 'secret'],
      'enum': ['enum'],
      'string-list': ['stringList'],
      'custom': ['custom'],
    }
    let checked = 0
    for (const key of golden) {
      for (const field of key.fields) {
        const control = rowFor(key.key, field.name)?.control
        expect(control, `${key.key}.${field.name} renders no row`).toBeDefined()
        if (control === undefined)
          continue
        const allowed = kindsFor[field.kind]
        expect(allowed, `no control kind is declared for the Go kind ${field.kind}`).toBeDefined()
        expect(
          allowed,
          `${key.key}.${field.name} is ${field.kind} on the hub but ${control.kind} here`,
        ).toContain(control.kind)
        checked++
      }
    }
    expect(checked, 'the parity check must actually compare some fields').toBeGreaterThan(0)
  })

  // Each bound is compared on its own, and the undeclared half is asserted
  // ABSENT rather than skipped: a one-sided bound is the normal shape on the
  // hub, and `toBe(field.min ?? control.min)` compares a control against
  // ITSELF wherever the hub declares no minimum.
  it('carries each numeric bound through from the hub', () => {
    let checked = 0
    for (const key of golden) {
      for (const field of key.fields) {
        if (field.min === undefined && field.max === undefined)
          continue
        const control = rowFor(key.key, field.name)?.control
        if (control?.kind !== 'number' && control?.kind !== 'slider')
          continue
        if (field.min === undefined) {
          expect(control.min, `${key.key}.${field.name} declares no minimum on the hub`).toBeUndefined()
        }
        else {
          expect(control.min, `${key.key}.${field.name} min`).toBe(field.min)
          checked++
        }
        if (field.max === undefined) {
          expect(control.max, `${key.key}.${field.name} declares no maximum on the hub`).toBeUndefined()
        }
        else {
          expect(control.max, `${key.key}.${field.name} max`).toBe(field.max)
          checked++
        }
      }
    }
    expect(checked, 'the parity check must actually compare some bounds').toBeGreaterThan(0)
  })

  // A declared UNIT picks a control; it does not merely word one. An
  // integer field declared in percent renders a slider, and every other
  // unit rides on a number box. The golden records the unit, so this reads
  // the hub's own declaration -- while it did not, this suite had to
  // restate "percent" by hand to build a fixture that rendered what
  // production renders.
  it('builds the control each declared unit calls for', () => {
    let checked = 0
    for (const key of golden) {
      for (const field of key.fields) {
        if (field.unit === undefined || field.unit === '')
          continue
        const control = rowFor(key.key, field.name)?.control
        expect(control, `${key.key}.${field.name} renders no row`).toBeDefined()
        const percent = field.unit === 'percent' || field.unit === '%'
        expect(
          control?.kind,
          `${key.key}.${field.name} is declared in ${field.unit}`,
        ).toBe(percent ? 'slider' : 'number')
        if (control?.kind === 'slider' || control?.kind === 'number')
          expect(control.unit).toBe(percent ? '%' : field.unit)
        checked++
      }
    }
    expect(checked, 'the parity check must actually compare some units').toBeGreaterThan(0)
  })

  // The custom-editor id picks a control too. `controlForField` refuses an
  // id this client does not carry rather than inventing a text box over an
  // opaque value, so the id decides whether the row exists at all.
  it('builds the custom editor the hub names', () => {
    let checked = 0
    for (const key of golden) {
      for (const field of key.fields) {
        if (field.kind !== 'custom')
          continue
        expect(
          field.customId,
          `${key.key}.${field.name} is custom but the golden names no editor`,
        ).toBeTruthy()
        expect(rowFor(key.key, field.name)?.control)
          .toEqual({ kind: 'custom', id: field.customId })
        checked++
      }
    }
    expect(checked, 'the parity check must actually compare some custom editors').toBeGreaterThan(0)
  })

  // The other half of the same rule: an editor id this client does not
  // carry renders NO row. A fixture that omitted the id therefore lost the
  // whole keyboard-shortcuts group while production rendered it, which is
  // what makes the id a fact the golden has to hold.
  it('drops a custom field whose editor this client does not carry', () => {
    const renamed = wire.map(d => (d.key === 'keybindings'
      ? { ...d, fields: [{ ...d.fields[0], customId: 'from-a-newer-hub' }] }
      : d)) as ProtoSettingDescriptor[]
    const ids = descriptorsOf(makeFakePrefs() as unknown as PreferencesState, renamed).map(d => d.id)
    expect(ids).not.toContain('shortcuts.keybindings')
    expect(ids).toContain('appearance.theme')
  })

  // Every account row still needs the text a user reads, which the wire
  // does not carry: `usersettings/keys.go` declares no field label and no
  // enum label deliberately. An object-shaped key is where that bites --
  // the hub names the KEY ("UI fonts") and not its two fields, so a row
  // that fell back to the title would put both halves under one name.
  it('names every account row, and names the two halves of a key apart', () => {
    const labels = new Map<string, string[]>()
    for (const key of golden) {
      for (const field of key.fields) {
        const row = rowFor(key.key, field.name)
        expect(row?.label, `${key.key}.${field.name} renders no row`).toBeTruthy()
        labels.set(key.key, [...(labels.get(key.key) ?? []), row!.label])
      }
    }
    const objectShaped = [...labels].filter(([, names]) => names.length > 1)
    expect(objectShaped.length, 'no object-shaped key is being compared').toBeGreaterThan(0)
    for (const [key, names] of objectShaped)
      expect(new Set(names).size, `${key} renders two rows under one name`).toBe(names.length)
  })
})

// The wire decides which options exist; the declaration only names them.
// So the registry cannot offer a value the hub's validator refuses, which
// is the drift the golden file existed to catch.
describe('createBrowserRows enum option names', () => {
  const prefs = () => makeFakePrefs() as unknown as PreferencesState

  // `diff_view` is the representative dual ENUM row. `theme` and
  // `terminal_theme` both used to play that part and cannot any more: each is
  // one object key edited by a custom editor, so neither declares enum values
  // for the join to read.
  const ENUM_KEY = 'diff_view'
  const ENUM_ROW = 'appearance.diffView'

  /** The wire, with the enum row carrying the given values instead. */
  function enumValues(...values: string[]): ProtoSettingDescriptor[] {
    return wire.map(d => (d.key === ENUM_KEY
      ? { ...d, fields: [{ ...d.fields[0], enumValues: values.map(v => ({ value: v, label: '', help: '' })) }] }
      : d)) as ProtoSettingDescriptor[]
  }

  function enumOptions(descriptors: ProtoSettingDescriptor[]) {
    const control = descriptorsOf(prefs(), descriptors).find(d => d.id === ENUM_ROW)?.control
    return control?.kind === 'enum' ? control.options : undefined
  }

  it('names each declared value and keeps the wire order', () => {
    expect(enumOptions(wire)).toEqual([
      { value: 'unified', label: 'Unified' },
      { value: 'split', label: 'Side by side' },
    ])
  })

  // A value a newer hub adds is offered at once, under its own slug. The
  // alternative -- a client-side option list -- refuses a value the hub
  // stores, and a user who set it elsewhere cannot see it here.
  it('offers a value the registry has no name for, under its slug', () => {
    expect(enumOptions(enumValues('unified', 'inline'))).toEqual([
      { value: 'unified', label: 'Unified' },
      { value: 'inline', label: 'inline' },
    ])
  })

  // The reverse: a value the hub drops takes its stale name out with it,
  // rather than leaving an option whose write the hub refuses.
  it('drops an option the hub stopped declaring', () => {
    expect(enumOptions(enumValues('unified'))?.map(o => o.value)).toEqual(['unified'])
  })

  // The hub declares no enum label today, so this is what happens when it
  // starts: the wire wins, with no edit to the declarations.
  it('prefers the hub label over the declared one', () => {
    const labelled = wire.map(d => (d.key === ENUM_KEY
      ? { ...d, fields: [{ ...d.fields[0], enumValues: [{ value: 'unified', label: 'One column', help: '' }] }] }
      : d)) as ProtoSettingDescriptor[]
    expect(enumOptions(labelled)).toEqual([{ value: 'unified', label: 'One column' }])
  })

  // No theme row is an enum row: each renders the whole-value custom editor the
  // hub declares for it. A regression to an enum control here would mean a
  // palette list had moved back into the wire, where it cannot live.
  //
  // All THREE, not two. The syntax row arrived last and was the one left
  // unasserted, so a wire or registry mismatch that turned it back into an enum
  // -- or dropped its custom editor -- would have shipped green.
  it('renders every theme row as its custom editor, not as an enum', () => {
    const rows = descriptorsOf(prefs(), wire)
    expect(rows.find(d => d.id === 'appearance.theme')?.control)
      .toEqual({ kind: 'custom', id: 'theme' })
    expect(rows.find(d => d.id === 'appearance.terminalTheme')?.control)
      .toEqual({ kind: 'custom', id: 'terminalTheme' })
    expect(rows.find(d => d.id === 'appearance.syntaxTheme')?.control)
      .toEqual({ kind: 'custom', id: 'syntaxTheme' })
  })
})

describe('createBrowserRows visibility', () => {
  /**
   * A prefs double carrying only the values the hide rules read.
   *
   * The turn-end sound is TWO tiers, because its volume row reads both:
   * `turnEndSound` is the account default and `turnEndSoundOverride` is
   * this device's override (null when the device follows the account).
   */
  function prefsWith(opts: {
    uiFontsEnabled?: boolean
    monoFontsEnabled?: boolean
    turnEndSound?: string
    turnEndSoundOverride?: string | null
  }) {
    const base = makeFakePrefs()
    const account = opts.turnEndSound ?? 'ding-dong'
    const override = opts.turnEndSoundOverride ?? null
    const turnEndSound = {
      ...base.dual.turnEndSound,
      resolved: () => override ?? account,
      browser: () => override,
      account: () => account,
    }
    return {
      ...base,
      dual: { ...base.dual, turnEndSound },
      uiFonts: () => ({ enabled: opts.uiFontsEnabled ?? true, fonts: [] }),
      monoFonts: () => ({ enabled: opts.monoFontsEnabled ?? true, fonts: [] }),
      // The context publishes the resolved reader as a top-level accessor
      // too. Taken FROM the dual entry, so the double cannot state one
      // resolved value at one address and a different one at the other.
      turnEndSound: turnEndSound.resolved,
    } as unknown as PreferencesState
  }

  function visible(prefs: PreferencesState, id: string): boolean {
    const d = descriptorsOf(prefs).find(x => x.id === id)
    expect(d, `${id} must exist`).toBeDefined()
    return !(d!.hidden?.() ?? false)
  }

  // The panels this registry replaced wrapped each of these in a <Show>.
  // Without the rule the row renders, the user edits it, and the edit has
  // no effect — the tier that consumes it is switched off.
  it('hides a font stack while its tier is disabled', () => {
    expect(visible(prefsWith({ uiFontsEnabled: false }), 'appearance.uiFontStack')).toBe(false)
    expect(visible(prefsWith({ uiFontsEnabled: true }), 'appearance.uiFontStack')).toBe(true)
    expect(visible(prefsWith({ monoFontsEnabled: false }), 'appearance.monoFontStack')).toBe(false)
    expect(visible(prefsWith({ monoFontsEnabled: true }), 'appearance.monoFontStack')).toBe(true)
  })

  it('hides the turn-end volume while no sound is selected', () => {
    expect(visible(prefsWith({ turnEndSound: 'none' }), 'notifications.turnEndSoundVolume')).toBe(false)
    expect(visible(prefsWith({ turnEndSound: 'ding-dong' }), 'notifications.turnEndSoundVolume')).toBe(true)
  })

  // The volume row edits BOTH tiers, so one silent tier must not hide it.
  // A user who mutes the sound on the laptop still sets the volume the
  // PHONE plays the account default at; a device that overrides a silent
  // account default with a real sound needs the volume for that device.
  // The row hides only when NEITHER tier can play anything.
  it('keeps the turn-end volume while either tier still plays a sound', () => {
    const deviceMuted = prefsWith({ turnEndSound: 'ding-dong', turnEndSoundOverride: 'none' })
    expect(visible(deviceMuted, 'notifications.turnEndSoundVolume')).toBe(true)

    const accountMuted = prefsWith({ turnEndSound: 'none', turnEndSoundOverride: 'ding-dong' })
    expect(visible(accountMuted, 'notifications.turnEndSoundVolume')).toBe(true)

    const bothMuted = prefsWith({ turnEndSound: 'none', turnEndSoundOverride: 'none' })
    expect(visible(bothMuted, 'notifications.turnEndSoundVolume')).toBe(false)
  })

  it('keeps the enabling toggles themselves visible', () => {
    const off = prefsWith({ uiFontsEnabled: false, monoFontsEnabled: false, turnEndSound: 'none' })
    expect(visible(off, 'appearance.uiFonts')).toBe(true)
    expect(visible(off, 'appearance.monoFonts')).toBe(true)
    expect(visible(off, 'notifications.turnEndSound')).toBe(true)
  })

  it('yields one descriptor per entry, in declaration order', () => {
    expect(descriptorsOf(prefsWith({})).map(d => d.id))
      .toEqual(browserSettings.map(d => d.id))
  })
})
