import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal, Show } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { accountWireDescriptors } from '~/test-support/accountSchema'
import { stubMatchMedia } from '~/test-support/matchMediaStub'

import { PreferencesDialog } from './PreferencesDialog'

const listUserSettings = vi.hoisted(() => vi.fn().mockResolvedValue({ descriptors: [], values: [] }))
const listSettings = vi.hoisted(() => vi.fn().mockResolvedValue({ descriptors: [], values: [] }))
const updateSetting = vi.hoisted(() => vi.fn())
const isAdmin = vi.hoisted(() => vi.fn(() => false))
const solo = vi.hoisted(() => vi.fn(() => false))
const elevationExpiresAt = vi.hoisted(() => vi.fn((): { seconds: bigint, nanos: number } | undefined => undefined))

vi.mock('~/api/clients', () => ({
  userClient: { listUserSettings, updateUserSetting: vi.fn(), resetUserSetting: vi.fn() },
  adminSettingsClient: { listSettings, updateSetting, updateSettingSecret: vi.fn(), resetSetting: vi.fn() },
  authClient: {},
}))

// The elevation members are REAL here, not stubs: the panel renders the
// verified-session state at the top of every group that holds an
// elevation-guarded row, and `elevationExpiresAt` is what decides whether that
// state exists. A test drives it through the hoisted mock.
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => ({ username: 'admin', isAdmin: isAdmin() }),
    elevationExpiresAt: () => elevationExpiresAt(),
    setElevationExpiresAt: vi.fn(),
    refreshUser: vi.fn().mockResolvedValue(undefined),
  }),
}))

vi.mock('~/lib/systemInfo', async importOriginal => ({
  ...(await importOriginal<typeof import('~/lib/systemInfo')>()),
  isSoloMode: () => solo(),
  isDesktopApp: () => false,
}))

function renderDialog(category = 'appearance') {
  return render(() => (
    <PreferencesProvider>
      <PreferencesDialog category={category} openSeq={0} onClose={() => {}} />
    </PreferencesProvider>
  ))
}

beforeEach(() => {
  // Call HISTORY, not the implementations (`vi.clearAllMocks` leaves those
  // alone). A test that counts the loads a dialog issues reads a per-test
  // number, rather than the running total of every test before it.
  vi.clearAllMocks()
  // Every account row takes its SHAPE from this reply: the hub declares
  // each key's category, control kind, enum values and bounds, and the
  // registry joins only the text a user reads onto it. A dialog that wants
  // Theme, the font stacks or the keyboard-shortcuts editor therefore needs
  // the real schema here.
  listUserSettings.mockResolvedValue({ descriptors: accountWireDescriptors(), values: [] })
  listSettings.mockResolvedValue({ descriptors: [], values: [] })
  isAdmin.mockReturnValue(false)
  solo.mockReturnValue(false)
  elevationExpiresAt.mockReturnValue(undefined)
})

afterEach(() => {
  cleanup()
})

describe('preferencesDialog admin gating', () => {
  it('shows the administration groups only to admins', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [{
        key: 'session_duration_seconds',
        category: 'general',
        title: 'Session duration',
        summary: '',
        order: 10,
        hiddenInSolo: false,
        restart: false,
        fields: [{ name: '', label: 'Session duration', help: '', kind: 2, enumValues: [], unit: 'seconds', secret: false, placeholder: '', min: 300n }],
      }],
      values: [],
    })
    renderDialog('admin-general')
    await waitFor(() => expect(screen.getByText('ADMINISTRATION')).toBeTruthy())
    expect(screen.getByText('PREFERENCES')).toBeTruthy()
    expect(screen.getByTestId('preferences-nav-admin-general')).toBeTruthy()
    expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy()

    cleanup()
    isAdmin.mockReturnValue(false)
    renderDialog()
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(screen.queryByTestId('preferences-nav-admin-general')).toBeNull()
    expect(screen.queryByText('ADMINISTRATION')).toBeNull()
    expect(screen.getByText('PREFERENCES')).toBeTruthy()
  })

  it('marks admin groups that hold restart-class settings', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [{
        key: 'queue_budget',
        category: 'advanced',
        title: 'Queue budgets',
        summary: '',
        order: 20,
        hiddenInSolo: false,
        restart: true,
        fields: [{ name: 'relay_bytes', label: 'Relay', help: '', kind: 2, enumValues: [], unit: 'bytes', secret: false, placeholder: '' }],
      }],
      values: [],
    })
    renderDialog('admin-advanced')
    await waitFor(() => expect(screen.getByTestId('preferences-nav-admin-advanced').textContent).toContain('\u26A0'))
  })

  // Derived from the VISIBLE rows, in ONE place that both the nav mark and
  // the panel warning read: a group whose restart rows are all hidden has
  // nothing in it that needs a restart, so saying so would be false.
  it('neither marks nor warns when the only restart row is hidden', async () => {
    isAdmin.mockReturnValue(true)
    solo.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [
        {
          key: 'session_duration_seconds',
          category: 'general',
          title: 'Session duration',
          summary: '',
          order: 10,
          hiddenInSolo: false,
          restart: false,
          fields: [{ name: '', label: 'Session duration', help: '', kind: 2, enumValues: [], unit: 'seconds', secret: false, placeholder: '', min: 300n }],
        },
        {
          key: 'hidden_restart_setting',
          category: 'general',
          title: 'Hidden restart setting',
          summary: '',
          order: 20,
          hiddenInSolo: true,
          restart: true,
          fields: [{ name: '', label: 'Hidden restart setting', help: '', kind: 1, enumValues: [], unit: '', secret: false, placeholder: '' }],
        },
      ],
      values: [],
    })
    renderDialog('admin-general')
    await waitFor(() => expect(screen.getByText('Session duration')).toBeTruthy())
    expect(screen.getByTestId('preferences-nav-admin-general').textContent).not.toContain('\u26A0')
    expect(screen.queryByText('Changes in this group apply after a hub restart.')).toBeNull()
  })

  // The nav badge and the panel warning are derived from ONE row list, so a
  // restart-class row marks both whichever source it came from. Both used to
  // read `group.admin ? adminRows() : []`, so a USER-scope restart row got
  // neither -- the badge in the list and the warning in the panel agreed
  // only because they were equally blind.
  it('marks a USER group whose row the hub declares restart-class', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: accountWireDescriptors().map(d => (d.key === 'theme' ? { ...d, restart: true } : d)),
      values: [],
    })
    renderDialog('appearance')
    await waitFor(() =>
      expect(screen.getByText('Changes in this group apply after a hub restart.')).toBeTruthy())
    expect(screen.getByTestId('preferences-nav-appearance').textContent).toContain('⚠')
    // Only the group that holds it, not every user group.
    expect(screen.getByTestId('preferences-nav-chat').textContent).not.toContain('⚠')
  })

  it('renders the restart warning on restart groups', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [{
        key: 'queue_budget',
        category: 'advanced',
        title: 'Queue budgets',
        summary: '',
        order: 20,
        hiddenInSolo: false,
        restart: true,
        fields: [{ name: 'relay_bytes', label: 'Relay', help: '', kind: 2, enumValues: [], unit: 'bytes', secret: false, placeholder: '' }],
      }],
      values: [{ key: 'queue_budget', valueJson: '', effectiveJson: '{"relay_bytes":0}', customized: false, secretSet: {} }],
    })
    renderDialog('admin-advanced')
    await waitFor(() => expect(screen.getByText('Changes in this group apply after a hub restart.')).toBeTruthy())
  })
})

describe('preferencesDialog deep link', () => {
  // Every entry point (the app menu, $mod+, and the user menu) passes the
  // same category, and a signal notifies only on a CHANGE — so asking for
  // Preferences again while it sat on another section wrote the same string
  // and moved nothing. The request COUNT is what changes on a repeat.
  it('returns to the requested section when Preferences is asked for again', async () => {
    const [seq, setSeq] = createSignal(0)
    render(() => (
      <PreferencesProvider>
        <PreferencesDialog category="appearance" openSeq={seq()} onClose={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())

    fireEvent.click(screen.getByTestId('preferences-nav-notifications'))
    await waitFor(() =>
      expect(screen.getByTestId('preferences-nav-notifications').getAttribute('aria-selected')).toBe('true'))

    setSeq(n => n + 1)
    await waitFor(() =>
      expect(screen.getByTestId('preferences-nav-appearance').getAttribute('aria-selected')).toBe('true'))
    expect(screen.getByTestId('preferences-nav-notifications').getAttribute('aria-selected')).toBe('false')
  })

  // The dialog unmounts on close, so an unpaired media-query listener
  // accumulated one handler per open, each writing into a disposed scope.
  it('removes its viewport listener on close, every time', async () => {
    const mm = stubMatchMedia()
    try {
      const query = '(max-width: 639px)'
      renderDialog()
      await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
      expect(mm.handlersFor(query)).toHaveLength(1)
      cleanup()
      expect(mm.handlersFor(query)).toHaveLength(0)

      renderDialog()
      await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
      expect(mm.handlersFor(query)).toHaveLength(1)
      cleanup()
      expect(mm.handlersFor(query)).toHaveLength(0)
    }
    finally {
      mm.restore()
    }
  })
})

describe('preferencesDialog solo mode', () => {
  it('hides the Account category but keeps browser rows and their scope chip', async () => {
    solo.mockReturnValue(true)
    renderDialog()
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(screen.queryByTestId('preferences-nav-account')).toBeNull()
    // Dual rows still render with the scope chip.
    expect(screen.getByTestId('scope-chip-appearance.theme')).toBeTruthy()
  })

  it('hides administration groups that have no visible rows', async () => {
    isAdmin.mockReturnValue(true)
    solo.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [
        {
          key: 'session_duration_seconds',
          category: 'general',
          title: 'Session duration',
          summary: '',
          order: 10,
          hiddenInSolo: false,
          restart: false,
          fields: [{ name: '', label: 'Session duration', help: '', kind: 2, enumValues: [], unit: 'seconds', secret: false, placeholder: '', min: 300n }],
        },
        {
          key: 'captcha.enabled',
          category: 'captcha',
          title: 'Bot protection enabled',
          summary: '',
          order: 10,
          hiddenInSolo: true,
          restart: false,
          fields: [{ name: '', label: 'Bot protection enabled', help: '', kind: 1, enumValues: [], unit: '', secret: false, placeholder: '' }],
        },
        {
          key: 'rate_limit.elevation',
          category: 'rate-limits',
          title: 'Rate limit',
          summary: '',
          order: 10,
          hiddenInSolo: true,
          restart: false,
          fields: [{ name: 'enabled', label: 'Enabled', help: '', kind: 1, enumValues: [], unit: '', secret: false, placeholder: '' }],
        },
      ],
      values: [],
    })
    renderDialog('admin-general')
    await waitFor(() => expect(screen.getByTestId('preferences-nav-admin-general')).toBeTruthy())
    expect(screen.queryByTestId('preferences-nav-admin-captcha')).toBeNull()
    expect(screen.queryByTestId('preferences-nav-admin-rate-limits')).toBeNull()
  })

  it('falls back when a deep link targets a solo-hidden admin group', async () => {
    isAdmin.mockReturnValue(true)
    solo.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [
        {
          key: 'session_duration_seconds',
          category: 'general',
          title: 'Session duration',
          summary: '',
          order: 10,
          hiddenInSolo: false,
          restart: false,
          fields: [{ name: '', label: 'Session duration', help: '', kind: 2, enumValues: [], unit: 'seconds', secret: false, placeholder: '', min: 300n }],
        },
        {
          key: 'captcha.enabled',
          category: 'captcha',
          title: 'Bot protection enabled',
          summary: '',
          order: 10,
          hiddenInSolo: true,
          restart: false,
          fields: [{ name: '', label: 'Bot protection enabled', help: '', kind: 1, enumValues: [], unit: '', secret: false, placeholder: '' }],
        },
      ],
      values: [],
    })
    renderDialog('admin-captcha')
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(screen.queryByTestId('preferences-nav-admin-captcha')).toBeNull()
    expect(screen.getByTestId('preferences-nav-appearance').getAttribute('aria-selected')).toBe('true')
  })
})

describe('preferencesDialog search', () => {
  it('focuses the search box on / when not already in an input', async () => {
    renderDialog()
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    fireEvent.keyDown(document, { key: '/' })
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId('preferences-search')))
  })

  it('hides the navigation while searching and groups results by category', async () => {
    renderDialog()
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    const search = screen.getByTestId('preferences-search') as HTMLInputElement
    fireEvent.input(search, { target: { value: 'volume' } })
    await waitFor(() => expect(screen.queryByTestId('preferences-nav-appearance')).toBeNull())
    await waitFor(() => expect(screen.getByTestId('preferences-search-results')).toBeTruthy())
    // Turn-end sound (keyword) + Turn-end volume (label) both hit.
    expect(screen.getByText(/Turn-end sound/)).toBeTruthy()
    expect(screen.getByText(/Turn-end volume/)).toBeTruthy()
  })

  it('escape clears the search before closing the dialog', async () => {
    const onClose = vi.fn()
    render(() => (
      <PreferencesProvider>
        <PreferencesDialog category="appearance" openSeq={0} onClose={onClose} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    const search = screen.getByTestId('preferences-search') as HTMLInputElement
    fireEvent.input(search, { target: { value: 'theme' } })
    await waitFor(() => expect(screen.getByTestId('preferences-search-results')).toBeTruthy())

    fireEvent.keyDown(search, { key: 'Escape' })
    await waitFor(() => expect(search.value).toBe(''))
    // Navigation is back; the dialog was not asked to close.
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(onClose).not.toHaveBeenCalled()
  })

  it('shows an empty-results message when nothing matches', async () => {
    renderDialog()
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    const search = screen.getByTestId('preferences-search') as HTMLInputElement
    fireEvent.input(search, { target: { value: 'xyzzy-no-such-setting' } })
    await waitFor(() => expect(screen.getByText(/No settings match/)).toBeTruthy())
    expect(screen.getByText(/xyzzy-no-such-setting/)).toBeTruthy()
  })

  it('selects the matching category and clears the query when a result is clicked', async () => {
    renderDialog('appearance')
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    const search = screen.getByTestId('preferences-search') as HTMLInputElement
    fireEvent.input(search, { target: { value: 'volume' } })
    await waitFor(() => expect(screen.getByTestId('preferences-search-results')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: /Turn-end volume/ }))
    await waitFor(() => expect(search.value).toBe(''))
    await waitFor(() => expect(screen.getByTestId('preferences-nav-notifications')).toBeTruthy())
    expect(screen.getByTestId('preferences-nav-notifications').getAttribute('aria-selected')).toBe('true')
  })
})

describe('preferencesDialog load failure', () => {
  // A failed admin load leaves zero descriptors, occupiedNavGroups then
  // drops every ADMINISTRATION group, and the dialog reads exactly like a
  // non-admin session. Saying so is the difference between "this hub is
  // broken" and "you are not an admin".
  it('states a failed admin load instead of silently showing no admin groups', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockRejectedValue(new Error('hub unavailable'))
    renderDialog()

    const alert = await waitFor(() => {
      const found = screen.getAllByRole('alert').find(el => el.getAttribute('data-variant') === 'error')
      expect(found).toBeTruthy()
      return found!
    })
    // The hub's own reason, not a generic fallback — that is what tells an
    // operator whether to retry or to look at the hub.
    expect(alert.textContent).toContain('hub unavailable')
    expect(screen.queryByTestId('preferences-nav-admin-general')).toBeNull()
  })

  it('says nothing when the load succeeds', async () => {
    isAdmin.mockReturnValue(true)
    renderDialog()
    await waitFor(() => expect(listSettings).toHaveBeenCalled())
    expect(screen.queryAllByRole('alert').filter(el => el.getAttribute('data-variant') === 'error')).toEqual([])
  })

  /** The dialog's one error banner, once it appears. */
  async function errorAlert() {
    return waitFor(() => {
      const found = screen.getAllByRole('alert').find(el => el.getAttribute('data-variant') === 'error')
      expect(found).toBeTruthy()
      return found!
    })
  }

  // The hub declares every account key's shape, so a failed load leaves the
  // dialog with NO account row to build -- rather than a row at a control
  // and a default this client invented, which a user would then save over
  // their real stored value. The banner is what makes the absence legible.
  it('states a failed account load and renders no account row', async () => {
    listUserSettings.mockRejectedValue(new Error('account settings unavailable'))
    renderDialog('notifications')

    const alert = await errorAlert()
    expect(alert.textContent).toContain('account settings unavailable')
    expect(alert.textContent).toContain('Settings saved to your account are not listed')

    // The browser-only row of this group survives; its two account rows do
    // not, and Appearance holds account rows ONLY, so that group drops out.
    expect(screen.getByText('Terminal OS notifications')).toBeTruthy()
    expect(screen.queryByText('Turn-end sound')).toBeNull()
    expect(screen.queryByText('Turn-end volume')).toBeNull()
    expect(screen.queryByTestId('preferences-nav-appearance')).toBeNull()
    expect(screen.queryByTestId('preferences-nav-shortcuts')).toBeNull()
  })

  // The context loads once, at PROVIDER mount. A load that failed with no
  // identity change behind it -- an unreachable hub, a timeout, a 500 -- has
  // nothing else to retry it: `usePreferencesForIdentity` fires
  // on a sign-in, and this page never has one. Every account row's shape
  // comes off that reply, so the dialog asks again rather than staying short
  // of two whole groups for the session.
  it('retries a failed account load when it is opened again', async () => {
    listUserSettings.mockRejectedValueOnce(new Error('account settings unavailable'))
    const [open, setOpen] = createSignal(true)
    render(() => (
      <PreferencesProvider>
        <Show when={open()}>
          <PreferencesDialog category="appearance" openSeq={0} onClose={() => {}} />
        </Show>
      </PreferencesProvider>
    ))
    await errorAlert()
    expect(screen.queryByTestId('preferences-nav-appearance')).toBeNull()

    setOpen(false)
    setOpen(true)
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(screen.getByText('Theme')).toBeTruthy()
    expect(screen.queryAllByRole('alert').filter(el => el.getAttribute('data-variant') === 'error')).toEqual([])
  })

  // A load that SUCCEEDED described a key set that does not change while the
  // page is open, so reopening the dialog must not ask again.
  it('does not ask again after a load that succeeded', async () => {
    const [open, setOpen] = createSignal(true)
    render(() => (
      <PreferencesProvider>
        <Show when={open()}>
          <PreferencesDialog category="appearance" openSeq={0} onClose={() => {}} />
        </Show>
      </PreferencesProvider>
    ))
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(listUserSettings).toHaveBeenCalledTimes(1)

    setOpen(false)
    setOpen(true)
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(listUserSettings).toHaveBeenCalledTimes(1)
  })
})

describe('preferencesDialog search index', () => {
  const captchaDescriptor = {
    key: 'captcha.turnstile',
    category: 'captcha',
    title: 'Cloudflare Turnstile',
    summary: '',
    order: 50,
    hiddenInSolo: true,
    restart: false,
    fields: [{
      name: 'site_key',
      label: 'Turnstile site key',
      help: '',
      kind: 4,
      enumValues: [],
      unit: '',
      secret: false,
      placeholder: '',
      dependsOn: { key: 'captcha.selected', field: '', in: ['turnstile'] },
    }],
  }

  // The index is built from the rows the panel renders, so "searchable"
  // and "visible" are the same predicate. A hand-written second derivation
  // applied only half the visibility rules, and a hit then jumped to a
  // panel that does not show the row.
  it('omits a row whose dependsOn condition does not hold', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [captchaDescriptor],
      values: [{ key: 'captcha.selected', valueJson: '"altcha"', effectiveJson: '"altcha"', customized: false, secretSet: {} }],
    })
    renderDialog()
    await waitFor(() => expect(listSettings).toHaveBeenCalled())

    fireEvent.input(screen.getByTestId('preferences-search'), { target: { value: 'Turnstile site key' } })
    const results = await waitFor(() => screen.getByTestId('preferences-search-results'))
    // Assert on the RESULT BUTTONS: the empty-state message echoes the query
    // back, so a text match on the query proves nothing either way.
    expect(results.querySelectorAll('button')).toHaveLength(0)
  })

  it('includes the same row once its condition holds', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [captchaDescriptor],
      values: [{ key: 'captcha.selected', valueJson: '"turnstile"', effectiveJson: '"turnstile"', customized: false, secretSet: {} }],
    })
    renderDialog()
    await waitFor(() => expect(listSettings).toHaveBeenCalled())

    fireEvent.input(screen.getByTestId('preferences-search'), { target: { value: 'Turnstile site key' } })
    const results = await waitFor(() => screen.getByTestId('preferences-search-results'))
    await waitFor(() => expect(results.querySelectorAll('button').length).toBeGreaterThan(0))
    expect(results.textContent).toContain('Turnstile site key')
  })
})

describe('a failed account-settings load', () => {
  // The context fetches the account settings ONCE at mount and hands the
  // reply to every reader. The dialog's own store reads that reply rather
  // than asking again, so it can never fail — which left a failed load
  // completely silent: every row rendered its built-in default, and the
  // first change the user made overwrote their real stored value.
  it('is stated in the dialog', async () => {
    listUserSettings.mockRejectedValue(new Error('network is unreachable'))
    renderDialog()

    await waitFor(() => {
      expect(screen.getByText(/network is unreachable/)).toBeTruthy()
    })
  })

  it('leaves no error banner when the load succeeds', async () => {
    renderDialog()
    await waitFor(() => expect(screen.getByText('Appearance')).toBeTruthy())
    expect(screen.queryByText(/Failed to load account settings/)).toBeNull()
  })
})

/**
 * The one hub key both describes below need: a scalar bool whose read-time
 * rule (dev mode holds sign-up open) makes the applied value differ from
 * the configured one.
 */
const signupDescriptor = {
  key: 'signup_enabled',
  category: 'signup',
  title: 'Open sign-up',
  summary: '',
  order: 10,
  hiddenInSolo: false,
  restart: false,
  fields: [{
    name: '',
    label: 'Open sign-up',
    help: '',
    kind: 1,
    enumValues: [],
    unit: '',
    secret: false,
    placeholder: '',
    customId: '',
  }],
}

/**
 * One wire value for that key. `mergedJson` DEFAULTS to the effective
 * document, which is the shape of every key no read-time rule touches; a
 * test about such a rule states the two documents apart.
 */
function signupValue(effectiveJson: string, overrides: Record<string, unknown> = {}) {
  return {
    key: 'signup_enabled',
    valueJson: '',
    mergedJson: effectiveJson,
    effectiveJson,
    customized: false,
    secretSet: {},
    ...overrides,
  }
}

describe('a hub-setting write keeps the row it came from', () => {
  // Building a control eagerly read a setting value, which made the row
  // memo depend on EVERY value. One successful write then rebuilt every
  // row object, and `<For>` reconciles by reference identity — so the write
  // re-created the DOM of the control that issued it. A keyboard user who
  // flips a toggle with Space lost focus to the document body.
  it('does not re-create the control the user just used', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [signupDescriptor],
      values: [signupValue('false')],
    })
    updateSetting.mockResolvedValue({ value: signupValue('true', { valueJson: 'true', customized: true }) })

    renderDialog('admin-signup')
    const toggle = await screen.findByRole('switch', { name: 'Open sign-up' })

    fireEvent.click(toggle)
    await waitFor(() => expect(screen.getByText('Customized')).toBeTruthy())

    expect(screen.getByRole('switch', { name: 'Open sign-up' })).toBe(toggle)
  })
})

/**
 * A row a read-time rule overrides shows BOTH facts: the control carries
 * the configured value, and the note beside it carries the value the hub
 * enforces.
 *
 * Both halves used to read the effective document, so the note printed the
 * control's own value straight back and the configured value never reached
 * the screen. Assert the two together, because either one alone passes
 * while the pair says nothing.
 */
describe('a hub setting a read-time rule overrides', () => {
  it('edits the configured value and notes the enforced one', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [signupDescriptor],
      // Nothing stored, so the code default is what an edit replaces; dev
      // mode holds sign-up open until an operator stores a row.
      values: [signupValue('true', { mergedJson: 'false' })],
    })

    renderDialog('admin-signup')
    const toggle = await screen.findByRole<HTMLInputElement>('switch', { name: 'Open sign-up' })

    expect(toggle.checked).toBe(false)
    // The note speaks the switch's own vocabulary, not the JSON literal
    // the wire carries.
    expect(screen.getByText(/Currently in effect:\s*On/)).toBeTruthy()
  })

  it('drops the note once the configured value is the enforced one', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue({
      descriptors: [signupDescriptor],
      values: [signupValue('true', { valueJson: 'true', mergedJson: 'true', customized: true })],
    })

    renderDialog('admin-signup')
    const toggle = await screen.findByRole<HTMLInputElement>('switch', { name: 'Open sign-up' })

    expect(toggle.checked).toBe(true)
    expect(screen.queryByText(/Currently in effect/)).toBeNull()
  })
})

/**
 * Account LEADS the navigation.
 *
 * It is the group a user opens the dialog for deliberately -- a password, a
 * passkey, an address -- where the rest are preferences they adjust while
 * they are already here. Asserted as a POSITION rather than as "is present",
 * because the group was always present; it simply sat eighth.
 */
describe('preferencesDialog section order', () => {
  it('puts Account first under PREFERENCES', async () => {
    renderDialog('appearance')
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())

    const tabs = screen.getAllByRole('tab').map(el => el.getAttribute('data-testid'))
    expect(tabs[0]).toBe('preferences-nav-account')
    expect(tabs).toContain('preferences-nav-appearance')
  })

  // Opening with no category lands on the first VISIBLE group, which is now
  // Account -- except in solo mode, where every account row is hidden and the
  // fallback must move on rather than render an empty panel.
  it('falls back past Account in solo mode', async () => {
    solo.mockReturnValue(true)
    renderDialog('account')
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(screen.queryByTestId('preferences-nav-account')).toBeNull()
    expect(screen.getByTestId('preferences-nav-appearance')).toHaveAttribute('aria-selected', 'true')
  })
})

/**
 * The verified-session state, at the top of every group whose rows the hub
 * refuses without a recently proven factor.
 *
 * It used to live inside ONE account editor, which put it half way down the
 * Account panel under a Save button it had nothing to do with -- and left
 * every ADMINISTRATION panel without it, although the same window governs
 * every hub-settings write.
 */
describe('preferencesDialog verified-session state', () => {
  const inTwoHours = () => ({ seconds: BigInt(Math.floor(Date.now() / 1000) + 7200), nanos: 0 })

  function adminGeneralSettings() {
    return {
      descriptors: [{
        key: 'session_duration_seconds',
        category: 'general',
        title: 'Session duration',
        summary: '',
        order: 10,
        hiddenInSolo: false,
        restart: false,
        fields: [{ name: '', label: 'Session duration', help: '', kind: 2, enumValues: [], unit: 'seconds', secret: false, placeholder: '', min: 300n }],
      }],
      values: [],
    }
  }

  it('shows it on an administration group while the session is verified', async () => {
    isAdmin.mockReturnValue(true)
    elevationExpiresAt.mockReturnValue(inTwoHours())
    listSettings.mockResolvedValue(adminGeneralSettings())

    renderDialog('admin-general')
    await waitFor(() => expect(screen.getByTestId('elevation-status')).toBeTruthy())
    expect(screen.getByTestId('elevation-drop')).toBeTruthy()
  })

  it('shows nothing on an administration group while the session is not', async () => {
    isAdmin.mockReturnValue(true)
    listSettings.mockResolvedValue(adminGeneralSettings())

    renderDialog('admin-general')
    await waitFor(() => expect(screen.getByText('Session duration')).toBeTruthy())
    expect(screen.queryByTestId('elevation-status')).toBeNull()
  })

  // A browser preference is not elevation-guarded, so a verified session is
  // not news there. Marking every group would make the state meaningless.
  it('shows nothing on a group with no elevation-guarded row', async () => {
    elevationExpiresAt.mockReturnValue(inTwoHours())

    renderDialog('appearance')
    await waitFor(() => expect(screen.getByTestId('preferences-nav-appearance')).toBeTruthy())
    expect(screen.queryByTestId('elevation-status')).toBeNull()
  })
})
