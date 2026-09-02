import type { SettingRowModel } from './types'
import type { PreferencesState } from '~/context/PreferencesContext'
import type { SettingDescriptor as ProtoSettingDescriptor, SettingField } from '~/generated/proto/leapmux/v1/settings_pb'
import type { SettingsStore } from '~/stores/settingsStore'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SettingFieldKind } from '~/generated/proto/leapmux/v1/settings_pb'
import { accountWireDescriptors } from '~/test-support/accountSchema'
import { makeFakePrefs } from '~/test-support/preferencesFake'
import { withPreferences } from '~/test-support/preferencesProvider'
import { buildProtoRows } from './protoRegistry'
import { CLAIMED_PROTO_KEYS, createBrowserRows } from './registry'
import { SettingsPanel } from './SettingsPanel'

// `isDesktopApp` is a controllable signal, defaulting to FALSE so every case
// below sees the browser environment it was written for. Only the Desktop
// group needs the other answer, and its rows exist nowhere else.
const isDesktopApp = vi.hoisted(() => vi.fn(() => false))
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  isDesktopApp: () => isDesktopApp(),
}))

// The panel renders the verified-session state at the top of an
// elevation-guarded group, and that state reads the auth context. Driven per
// test rather than stubbed to a constant, because "no window" and "a live
// window" are the two things the slot has to tell apart.
const elevationExpiresAt = vi.hoisted(() => vi.fn((): { seconds: bigint, nanos: number } | undefined => undefined))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    elevationExpiresAt: () => elevationExpiresAt(),
    dropElevation: vi.fn().mockResolvedValue(undefined),
    refreshUser: vi.fn().mockResolvedValue(undefined),
  }),
}))

function field(overrides: Partial<SettingField> = {}): SettingField {
  return {
    name: '',
    label: 'Test',
    help: '',
    kind: SettingFieldKind.BOOL,
    enumValues: [],
    unit: '',
    secret: false,
    placeholder: '',
    customId: '',
    ...overrides,
  } as unknown as SettingField
}

function protoDescriptor(overrides: Partial<ProtoSettingDescriptor> = {}): ProtoSettingDescriptor {
  return {
    key: 'ui_fonts',
    category: 'appearance',
    title: 'UI fonts',
    summary: '',
    order: 10,
    hiddenInSolo: false,
    restart: false,
    fields: [
      field({ name: 'enabled', label: 'Proto UI fonts enabled', kind: SettingFieldKind.BOOL }),
      field({ name: 'fonts', label: 'Proto UI font stack', kind: SettingFieldKind.STRING_LIST }),
    ],
    ...overrides,
  } as unknown as ProtoSettingDescriptor
}

/**
 * The registry rows the dialog would render, resolved against a prefs
 * double. There is no static list to read instead: a rule that depends on
 * another preference lives only here, so a second derivation would report
 * rows the dialog hides.
 */
function registryRows(): SettingRowModel[] {
  return createBrowserRows({
    ...makeFakePrefs(),
    uiFonts: () => ({ enabled: true, fonts: [] }),
    monoFonts: () => ({ enabled: true, fonts: [] }),
    turnEndSound: () => 'ding-dong',
  } as unknown as PreferencesState, accountWireDescriptors())
}

/**
 * One store double for both scopes. The hub scope is the only one with
 * store-backed rows now — every account key is declared in the browser
 * registry — but `buildProtoRows` takes any ProtoSettingsSource, and these
 * tests are about what the PANEL renders, not which scope supplied it.
 */
function fakeStore(descriptors: ProtoSettingDescriptor[] = []): SettingsStore {
  return {
    state: {
      descriptors,
      values: new Map(),
      loading: false,
      error: null,
      writeError: null,
      loaded: true,
    },
    values: () => new Map(),
    load: async () => {},
    update: async () => {},
    updateSecret: async () => {},
    reset: async () => {},
  }
}

afterEach(() => {
  cleanup()
})

/**
 * Build the rows the dialog would hand the panel for one category: the
 * same `buildProtoRows` + claimed-key + category + visibility filter, in
 * one place, so these tests exercise the panel against the rows it
 * actually receives.
 */
function rowsFor(store: SettingsStore, category: string, claimed = true): SettingRowModel[] {
  return buildProtoRows(store.state.descriptors, store)
    .filter(row => row.descriptor.category === category
      && (!claimed || !CLAIMED_PROTO_KEYS.has(row.protoKey))
      && !(row.descriptor.hidden?.() ?? false))
}

function registryFor(category: string): SettingRowModel[] {
  return registryRows().filter(({ descriptor }) =>
    descriptor.category === category && !(descriptor.hidden?.() ?? false))
}

// The wire gives these no distinct names: `controlForField` answers for every
// key of both scopes at once, so it builds a plain "Add" for every string list.
// Two identically named text boxes and two identically named buttons in one
// panel leave a screen-reader user unable to tell the UI stack from the
// monospace one, so the DECLARATION carries the name and the join keeps it.
describe('settingsPanel string-list accessible names', () => {
  it('gives the two font-stack add affordances distinct names', () => {
    render(withPreferences(() => (
      <SettingsPanel rows={registryFor('appearance')} restartGroup={false} elevationGroup={false} writeErrors={[]} />
    )))
    expect(screen.getByRole('textbox', { name: 'Add UI font' })).toBeTruthy()
    expect(screen.getByRole('textbox', { name: 'Add monospace font' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Add UI font' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Add monospace font' })).toBeTruthy()
    expect(screen.queryByRole('textbox', { name: 'Add' })).toBeNull()
  })
})

describe('settingsPanel claimed proto keys', () => {
  it('does not render hub-scope duplicates of object-shaped keys the registry already owns', () => {
    const store = fakeStore([protoDescriptor()])
    render(withPreferences(() => (
      <SettingsPanel
        rows={[...registryFor('appearance'), ...rowsFor(store, 'appearance')]}
        restartGroup={false}
        elevationGroup={false}
        writeErrors={[]}
      />
    )))
    expect(screen.getByText('Custom UI fonts')).toBeTruthy()
    expect(screen.queryByText('Proto UI fonts enabled')).toBeNull()
    expect(screen.queryByText('Proto UI font stack')).toBeNull()
    expect(screen.queryByText('Hub')).toBeNull()
  })

  it('still renders an unclaimed user-scope proto row in the matching category', () => {
    const store = fakeStore([protoDescriptor({
      key: 'unclaimed_appearance',
      fields: [field({ name: '', label: 'Unclaimed appearance toggle', kind: SettingFieldKind.BOOL })],
    })])
    render(withPreferences(() => (
      <SettingsPanel
        rows={[...registryFor('appearance'), ...rowsFor(store, 'appearance')]}
        restartGroup={false}
        elevationGroup={false}
        writeErrors={[]}
      />
    )))
    expect(screen.getByText('Unclaimed appearance toggle')).toBeTruthy()
  })

  it('an admin group renders hub rows and no registry rows', () => {
    const store = fakeStore([protoDescriptor({
      key: 'signup_enabled',
      category: 'signup',
      fields: [field({ name: '', label: 'Allow public signup', kind: SettingFieldKind.BOOL })],
    })])
    render(() => (
      <SettingsPanel
        rows={rowsFor(store, 'signup', false)}
        restartGroup={false}
        elevationGroup={false}
        writeErrors={[]}
      />
    ))
    expect(screen.getByText('Allow public signup')).toBeTruthy()
    expect(screen.queryByText('Custom UI fonts')).toBeNull()
  })

  it('does not paint a proto-key write error onto every field of an object setting', () => {
    const store = fakeStore([protoDescriptor({
      key: 'queue_budget',
      category: 'advanced',
      fields: [
        field({ name: 'relay_bytes', label: 'Queue budget - relay', kind: SettingFieldKind.INT }),
        field({ name: 'worker_bytes', label: 'Queue budget - worker', kind: SettingFieldKind.INT }),
        field({ name: 'userevents_bytes', label: 'Queue budget - user events', kind: SettingFieldKind.INT }),
      ],
    })])
    render(() => (
      <SettingsPanel
        rows={rowsFor(store, 'advanced', false)}
        restartGroup={false}
        elevationGroup={false}
        writeErrors={[{ key: 'queue_budget', message: 'queue budget relay_bytes must be 0 (auto-size) or at least 4194304 bytes' }]}
      />
    ))
    expect(screen.getByText('Queue budget - relay')).toBeTruthy()
    expect(screen.queryByTestId('setting-error-queue_budget.relay_bytes')).toBeNull()
    expect(screen.queryByTestId('setting-error-queue_budget.worker_bytes')).toBeNull()
    expect(screen.queryByTestId('setting-error-queue_budget.userevents_bytes')).toBeNull()
  })

  it('shows a scalar write error on the matching row only', () => {
    const store = fakeStore([protoDescriptor({
      key: 'session_duration_seconds',
      category: 'general',
      fields: [field({ name: '', label: 'Session duration', kind: SettingFieldKind.INT })],
    })])
    render(() => (
      <SettingsPanel
        rows={rowsFor(store, 'general', false)}
        restartGroup={false}
        elevationGroup={false}
        writeErrors={[{ key: 'session_duration_seconds', message: 'session duration must be at least 300s' }]}
      />
    ))
    expect(screen.getByTestId('setting-error-session_duration_seconds').textContent)
      .toContain('session duration must be at least 300s')
  })

  // The desktop shell's refusal is the one error a USER group carries, and a
  // registry row is addressed by its dotted id rather than by a hub key. The
  // tray toggle would otherwise read "on" beside no icon and no explanation,
  // which is the failure the Linux `recommends` decision made ordinary.
  it('shows the desktop shell refusal on its registry row only', () => {
    isDesktopApp.mockReturnValue(true)
    try {
      render(withPreferences(() => (
        <SettingsPanel
          rows={registryFor('desktop')}
          restartGroup={false}
          elevationGroup={false}
          writeErrors={[{ key: 'desktop.trayEnabled', message: 'LeapMux could not create a tray icon on this desktop' }]}
        />
      )))
      expect(screen.getByTestId('setting-error-desktop.trayEnabled').textContent)
        .toContain('LeapMux could not create a tray icon on this desktop')
      expect(screen.queryByTestId('setting-error-desktop.startOnLogin')).toBeNull()
    }
    finally {
      isDesktopApp.mockReturnValue(false)
    }
  })

  // The two refusable choices fail INDEPENDENTLY, and a Linux desktop with no
  // status-icon library is also a plausible one for the system to decline a
  // login item on. A channel carrying one would leave the other toggle reading
  // "on" with nothing behind it -- the exact silence it exists to remove.
  it('shows every refusal, each on the row that owns it', () => {
    isDesktopApp.mockReturnValue(true)
    try {
      render(withPreferences(() => (
        <SettingsPanel
          rows={registryFor('desktop')}
          restartGroup={false}
          elevationGroup={false}
          writeErrors={[
            { key: 'desktop.trayEnabled', message: 'no status-icon library' },
            { key: 'desktop.startOnLogin', message: 'the system declined the login item' },
          ]}
        />
      )))
      expect(screen.getByTestId('setting-error-desktop.trayEnabled').textContent)
        .toContain('no status-icon library')
      expect(screen.getByTestId('setting-error-desktop.startOnLogin').textContent)
        .toContain('the system declined the login item')
    }
    finally {
      isDesktopApp.mockReturnValue(false)
    }
  })
})

// The DERIVATION moved to the dialog, which marks the same groups in the
// nav from the same rule — see PreferencesDialog.test.tsx. What stays the
// panel's business is obeying it.
describe('settingsPanel restart warning', () => {
  const restartDescriptor = protoDescriptor({
    key: 'session_duration_seconds',
    category: 'general',
    restart: true,
    fields: [field({ name: '', label: 'Session duration', kind: SettingFieldKind.INT })],
  })

  it('warns when the group is a restart group', () => {
    const store = fakeStore([restartDescriptor])
    render(() => (
      <SettingsPanel
        rows={rowsFor(store, 'general', false)}
        restartGroup={true}
        elevationGroup={false}
        writeErrors={[]}
      />
    ))
    expect(screen.getByRole('alert').textContent).toContain('after a hub restart')
  })

  it('stays silent for a group that is not one', () => {
    const store = fakeStore([restartDescriptor])
    render(() => (
      <SettingsPanel
        rows={rowsFor(store, 'general', false)}
        restartGroup={false}
        elevationGroup={false}
        writeErrors={[]}
      />
    ))
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

/**
 * The verified-session state belongs to the PANEL, not to one editor inside
 * one group.
 *
 * It lived in an account editor, which put it half way down the Account panel
 * and left every ADMINISTRATION panel without it -- although the same window
 * governs every hub-settings write. The panel is the layer that knows which
 * group is on screen.
 */
describe('settingsPanel verified-session state', () => {
  const inTwoHours = () => ({ seconds: BigInt(Math.floor(Date.now() / 1000) + 7200), nanos: 0 })

  afterEach(() => elevationExpiresAt.mockReturnValue(undefined))

  it('shows it on an elevation group while the session is verified', () => {
    elevationExpiresAt.mockReturnValue(inTwoHours())
    render(() => (
      <SettingsPanel rows={[]} restartGroup={false} elevationGroup={true} writeErrors={[]} />
    ))
    expect(screen.getByTestId('elevation-status')).toBeTruthy()
  })

  it('shows nothing on an elevation group while the session is not verified', () => {
    render(() => (
      <SettingsPanel rows={[]} restartGroup={false} elevationGroup={true} writeErrors={[]} />
    ))
    expect(screen.queryByTestId('elevation-status')).toBeNull()
  })

  it('shows nothing on a group that holds no elevation-guarded row', () => {
    elevationExpiresAt.mockReturnValue(inTwoHours())
    render(() => (
      <SettingsPanel rows={[]} restartGroup={false} elevationGroup={false} writeErrors={[]} />
    ))
    expect(screen.queryByTestId('elevation-status')).toBeNull()
  })

  // Both notes are about the group as a whole, and the verified state is the
  // one that decides whether the rows below land without a prompt -- so it
  // reads first.
  it('puts it above the restart warning when a group carries both', () => {
    elevationExpiresAt.mockReturnValue(inTwoHours())
    const { container } = render(() => (
      <SettingsPanel rows={[]} restartGroup={true} elevationGroup={true} writeErrors={[]} />
    ))
    const alerts = [...container.querySelectorAll('[role="alert"]')]
    expect(alerts).toHaveLength(2)
    expect(alerts[0]!.textContent).toContain('This session is verified')
    expect(alerts[1]!.textContent).toContain('after a hub restart')
  })
})
