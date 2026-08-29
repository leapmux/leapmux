import { fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AppClientType, AppVisibility } from '~/generated/leapmux/v1/app_pb'
import { Scope } from '~/generated/leapmux/v1/scope_pb'
import { AppRegistrations } from './AppRegistrations'

const mockList = vi.fn()
const mockRegister = vi.fn()
const mockRevoke = vi.fn()
const mockUpdate = vi.fn()
const mockSetElevation = vi.fn()
const mockVerify = vi.fn()

vi.mock('~/api/clients', () => ({
  appClient: {
    listApps: (...args: unknown[]) => mockList(...args),
    registerApp: (...args: unknown[]) => mockRegister(...args),
    revokeApp: (...args: unknown[]) => mockRevoke(...args),
    updateApp: (...args: unknown[]) => mockUpdate(...args),
    setAppElevationAllowed: (...args: unknown[]) => mockSetElevation(...args),
    verifyApp: (...args: unknown[]) => mockVerify(...args),
  },
}))

// The vouch control reads the caller's role, and only an administrator's
// listing ever carries somebody else's app. The flag is set per test through
// vi.hoisted state, because the mock factory runs before the test body.
const authState = vi.hoisted(() => ({ admin: false }))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({ user: () => ({ id: 'u1', username: 'tester', isAdmin: authState.admin }) }),
}))

/** One registration, as the hub answers it. */
const app = {
  clientId: 'app-1',
  clientName: 'My integration',
  clientUri: 'https://example.com',
  visibility: AppVisibility.PRIVATE,
  clientType: AppClientType.PUBLIC,
  redirectUris: ['https://example.com/callback'],
  scopes: [Scope.WORKSPACE_READ, Scope.FILE_READ],
  grantTypes: ['authorization_code', 'refresh_token'],
  elevationAllowed: false,
  registrationSource: 'user',
  hasIcon: false,
  liveCredentialCount: 0n,
  createdAt: { seconds: 1767225600n, nanos: 0 },
  updatedAt: { seconds: 1767225600n, nanos: 0 },
}

describe('accountAppRegistrations', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authState.admin = false
    mockList.mockResolvedValue({ apps: [app], nextCursor: '', openRegistrationEnabled: false })
    mockRegister.mockResolvedValue({ app, clientSecret: '' })
    mockRevoke.mockResolvedValue({ revokedCredentialCount: 0n })
    mockUpdate.mockResolvedValue({ app })
    mockSetElevation.mockResolvedValue({ app })
    mockVerify.mockResolvedValue({ app })
  })

  it('lists the account registrations', async () => {
    render(() => <AppRegistrations />)
    expect(await screen.findByText('My integration')).toBeInTheDocument()
  })

  // UNVERIFIED is the default, and the consent screen warns a stranger about
  // it. The same fact appears here so the owner learns it before somebody else
  // meets it, rather than only where it is too late to act.
  it('marks an unverified registration', async () => {
    render(() => <AppRegistrations />)
    expect(await screen.findByText('unverified')).toBeInTheDocument()
  })

  it('names the administrator who vouched', async () => {
    mockList.mockResolvedValue({
      apps: [{ ...app, verified: true, verifiedAt: { seconds: 1767225600n, nanos: 0 }, verifiedByUsername: 'ada' }],
    })
    render(() => <AppRegistrations />)
    expect(await screen.findByText('verified by ada')).toBeInTheDocument()
    expect(screen.queryByText('unverified')).not.toBeInTheDocument()
  })

  // The CEILING is what any consent MAY grant, never what one account granted.
  // It renders as tokens derived from the generated enum, so a scope added to
  // the proto appears without an edit here.
  it('renders the permission ceiling as scope tokens', async () => {
    render(() => <AppRegistrations />)
    const ceiling = await screen.findByTestId('app-ceiling-app-1')
    expect(ceiling).toHaveTextContent('workspace:read')
    expect(ceiling).toHaveTextContent('file:read')
  })

  // The listing is keyset-paginated, and an administrator on a hub with open
  // registration can pass the default page size without lifting a finger. The
  // panel loops EVERY page, like the Connected-apps panel beside it: a
  // registration the panel never drew was one whose Edit and Retire controls
  // did not exist.
  it('loads every page of the listing', async () => {
    const pageTwo = { ...app, clientId: 'app-51', clientName: 'Page two app' }
    mockList.mockResolvedValueOnce({
      apps: [{ ...app, clientId: 'app-1', clientName: 'Page one app' }],
      nextCursor: 'cursor-2',
      openRegistrationEnabled: false,
    }).mockResolvedValueOnce({
      apps: [pageTwo],
      nextCursor: '',
      openRegistrationEnabled: false,
    })
    render(() => <AppRegistrations />)
    expect(await screen.findByText('Page one app')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('Page two app')).toBeInTheDocument())
    expect(mockList).toHaveBeenCalledTimes(2)
    expect(mockList).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'cursor-2' }))
  })

  it('reports an account with no registrations', async () => {
    mockList.mockResolvedValue({ apps: [] })
    render(() => <AppRegistrations />)
    expect(await screen.findByText(/No app registrations/)).toBeInTheDocument()
  })

  // The state of the hub-wide switch is stated beside what it affects, the
  // way app.proto documents the field -- and only when it is ON, because the
  // off state is the default nobody needs told about.
  it('states open registration beside the list, only while it is on', async () => {
    const { unmount } = render(() => <AppRegistrations />)
    expect(await screen.findByText('My integration')).toBeInTheDocument()
    expect(screen.queryByTestId('open-registration-note')).not.toBeInTheDocument()
    unmount()

    mockList.mockResolvedValue({ apps: [app], openRegistrationEnabled: true })
    render(() => <AppRegistrations />)
    expect(await screen.findByTestId('open-registration-note')).toHaveTextContent(/Open registration is on/)
  })

  // A built-in registration's fields are constants of the build, so the hub
  // refuses to retire one. The control says so through a tooltip rather than
  // being offered and then failing.
  it('refuses to retire a built-in registration', async () => {
    mockList.mockResolvedValue({
      apps: [{ ...app, registrationSource: 'builtin', clientName: 'LeapMux control CLI' }],
    })
    render(() => <AppRegistrations />)
    await screen.findByText('LeapMux control CLI')
    expect(screen.getByRole('button', { name: 'Retire' })).toBeDisabled()
  })

  it('retires after a confirmation and reports the cascade', async () => {
    mockRevoke.mockResolvedValue({ revokedCredentialCount: 3n })
    mockList
      .mockResolvedValueOnce({ apps: [app] })
      .mockResolvedValueOnce({ apps: [] })
    render(() => <AppRegistrations />)
    await screen.findByText('My integration')

    fireEvent.click(screen.getByRole('button', { name: 'Retire' }))
    const dialog = await screen.findByRole('dialog', { name: 'Retire this app?' })
    // The primary action is a ConfirmButton (danger): the first click arms
    // it, the second confirms.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Retire' }))
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Confirm?' }))

    await waitFor(() => expect(mockRevoke).toHaveBeenCalledWith({ clientId: 'app-1' }))
    // The COUNT reaches the reader: retiring an app takes credentials from
    // every account, and how many is the fact that makes it consequential.
    expect(await screen.findByText('App retired. 3 credentials revoked.')).toBeInTheDocument()
  })

  it('keeps the registration when the retire fails', async () => {
    mockRevoke.mockRejectedValue(new Error('the hub refused'))
    render(() => <AppRegistrations />)
    await screen.findByText('My integration')

    fireEvent.click(screen.getByRole('button', { name: 'Retire' }))
    const dialog = await screen.findByRole('dialog', { name: 'Retire this app?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Retire' }))
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Confirm?' }))

    expect(await screen.findByText(/the hub refused/)).toBeInTheDocument()
    expect(screen.getByText('My integration')).toBeInTheDocument()
  })

  // A hub that answers 500 must not render "No app registrations" beside the
  // error: telling somebody they have none while the truth is unknown is worse
  // than saying nothing.
  it('does not report an empty account when the load failed', async () => {
    mockList.mockRejectedValue(new Error('the hub is down'))
    render(() => <AppRegistrations />)
    expect(await screen.findByText(/the hub is down/)).toBeInTheDocument()
    expect(screen.queryByText(/No app registrations/)).not.toBeInTheDocument()
  })

  describe('registering', () => {
    it('registers a private app with the permissions ticked', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))
      fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'CI bot' } })
      fireEvent.click(screen.getByLabelText('workspace:read'))
      fireEvent.click(screen.getByRole('button', { name: 'Register' }))

      await waitFor(() => expect(mockRegister).toHaveBeenCalled())
      const sent = mockRegister.mock.calls[0]![0] as { clientName: string, visibility: number, scopes: number[] }
      expect(sent.clientName).toBe('CI bot')
      // PRIVATE always from this panel. A hub-wide registration needs an
      // administrator, and offering a control most accounts are refused would
      // be a worse answer than not offering it.
      expect(sent.visibility).toBe(1)
      expect(sent.scopes).toHaveLength(1)
    })

    // Register stays REFUSED until the form can produce a valid registration:
    // an app with no name has nothing for a consent screen to show, and one
    // with no permission asks for nothing.
    it('refuses to submit without a name and at least one permission', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')
      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))

      const submit = screen.getByRole('button', { name: 'Register' })
      expect(submit).toBeDisabled()

      fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'CI bot' } })
      expect(submit).toBeDisabled()

      fireEvent.click(screen.getByLabelText('workspace:read'))
      expect(submit).toBeEnabled()
    })

    // The app secret crosses ONCE. The hub stores its hash, so this signal
    // is the only place it exists on this machine -- which is why the form
    // stays open showing it instead of closing on success.
    it('shows a confidential app secret once and keeps the form open', async () => {
      mockRegister.mockResolvedValue({ app, clientSecret: 'the-secret-value' })
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))
      fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'CI bot' } })
      fireEvent.click(screen.getByLabelText('workspace:read'))
      fireEvent.click(screen.getByRole('button', { name: 'Register' }))

      expect(await screen.findByTestId('new-client-secret')).toHaveTextContent('the-secret-value')
      expect(screen.getByText(/Copy this app secret now/)).toBeInTheDocument()
      expect(screen.getByText(/cannot show it again/)).toBeInTheDocument()
      // The form has NOT closed, so the value cannot be lost by a re-render
      // the user did not ask for.
      expect(screen.getByTestId('register-app-form')).toBeInTheDocument()
    })

    // A PUBLIC client gets no secret at all, which is the honest answer for a
    // binary a user holds: PKCE is what protects it.
    it('shows no secret for a public app', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))
      fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'CI bot' } })
      fireEvent.click(screen.getByLabelText('workspace:read'))
      fireEvent.click(screen.getByRole('button', { name: 'Register' }))

      await waitFor(() => expect(mockRegister).toHaveBeenCalled())
      expect(screen.queryByTestId('new-client-secret')).not.toBeInTheDocument()
    })

    it('reports a refused registration and keeps what was typed', async () => {
      mockRegister.mockRejectedValue(new Error('client_name is required'))
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))
      fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'CI bot' } })
      fireEvent.click(screen.getByLabelText('workspace:read'))
      fireEvent.click(screen.getByRole('button', { name: 'Register' }))

      expect(await screen.findByText(/client_name is required/)).toBeInTheDocument()
      expect(screen.getByLabelText(/Name/)).toHaveValue('CI bot')
    })
  })

  // The choice is a PillGroup, never a native <select>: a select opens the OS
  // picker, ignores the app's theme, and renders text and nothing else -- so
  // the second line each choice needs would be impossible.
  it('offers the app type as a radiogroup', async () => {
    render(() => <AppRegistrations />)
    await screen.findByText('My integration')
    fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))

    expect(screen.getByRole('radiogroup', { name: 'App type' })).toBeInTheDocument()
    expect(screen.getAllByRole('radio')).toHaveLength(2)
    expect(document.querySelector('select')).toBeNull()
  })

  describe('the permission ceiling', () => {
    // The hub closes a grant at the mint, so a ceiling that states a write
    // without its read states a boundary the hub cannot deliver. The form
    // shows the closure: ticking the write implies the read, checked and
    // locked, and the request carries it.
    it('checks and locks the read a ticked write implies', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')
      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))

      fireEvent.click(screen.getByLabelText('workspace:write'))
      expect(screen.getByLabelText('workspace:read')).toBeChecked()
      expect(screen.getByLabelText('workspace:read')).toBeDisabled()

      fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'CI bot' } })
      fireEvent.click(screen.getByRole('button', { name: 'Register' }))
      await waitFor(() => expect(mockRegister).toHaveBeenCalled())
      const sent = mockRegister.mock.calls[0]![0] as { scopes: number[] }
      expect(sent.scopes).toEqual([Scope.WORKSPACE_READ, Scope.WORKSPACE_WRITE])
    })

    // The lock is derived from the ticked set, not stored beside it: untick
    // the write and the read it dragged in goes with it, rather than lurking
    // as a tick the owner never chose.
    it('frees the implied read when the write is unticked', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')
      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))

      fireEvent.click(screen.getByLabelText('workspace:write'))
      fireEvent.click(screen.getByLabelText('workspace:write'))

      expect(screen.getByLabelText('workspace:read')).not.toBeChecked()
      expect(screen.getByLabelText('workspace:read')).toBeEnabled()
    })

    // The catalogue groups by the families scope.proto itself sections into,
    // and the checkbox's accessible name stays the bare token -- the name a
    // consent screen and a stored grant both read.
    it('groups the scopes by family with each token labelable', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')
      fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))

      const fieldset = document.querySelector('fieldset')!
      expect(within(fieldset).getByText('Account')).toBeInTheDocument()
      expect(within(fieldset).getByText('Workspace')).toBeInTheDocument()
      expect(within(fieldset).getByText('Hub administration')).toBeInTheDocument()
      // The description sits OUTSIDE the label, so it cannot leak into the
      // checkbox's accessible name.
      const label = within(fieldset).getByText('workspace:read').closest('label')
      expect(label?.textContent).not.toContain('Read workspaces')
    })
  })

  describe('editing', () => {
    // The form opens PRE-FILLED: an edit that started blank would let one
    // stray Save strip the redirect addresses and the permission ceiling the
    // registration already had.
    it('opens the form pre-filled and replaces the editable fields', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
      const form = await screen.findByTestId('edit-app-form-app-1')
      expect(within(form).getByLabelText(/Name/)).toHaveValue('My integration')
      expect(within(form).getByLabelText(/Redirect addresses/)).toHaveValue('https://example.com/callback')
      expect(within(form).getByLabelText('workspace:read')).toBeChecked()
      expect(within(form).getByLabelText('agent:read')).not.toBeChecked()

      fireEvent.input(within(form).getByLabelText(/Name/), { target: { value: 'Renamed' } })
      fireEvent.click(within(form).getByLabelText('file:read'))
      fireEvent.click(within(form).getByLabelText('git:read'))
      fireEvent.click(within(form).getByRole('button', { name: 'Save' }))

      await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith({
        clientId: 'app-1',
        clientName: 'Renamed',
        clientUri: 'https://example.com',
        replaceRedirectUris: true,
        redirectUris: ['https://example.com/callback'],
        replaceScopes: true,
        // WORKSPACE_READ kept, FILE_READ dropped, GIT_READ added -- and
        // WORKER_READ is implied by git:read. The hub stores the
        // ceiling closed (RegisterApp runs scopes.Close()), so submitting the
        // bare ticked set and submitting this differ only in who did the
        // arithmetic.
        scopes: [Scope.WORKSPACE_READ, Scope.WORKER_READ, Scope.GIT_READ],
      }))
      expect(await screen.findByText('App updated.')).toBeInTheDocument()
    })

    // A built-in registration's fields are constants of the build: the hub
    // refuses the edit, so the control states that instead of failing.
    it('refuses to edit a built-in registration', async () => {
      mockList.mockResolvedValue({
        apps: [{ ...app, registrationSource: 'builtin', clientName: 'LeapMux control CLI' }],
      })
      render(() => <AppRegistrations />)
      await screen.findByText('LeapMux control CLI')
      expect(screen.getByRole('button', { name: 'Edit' })).toBeDisabled()
    })

    // A refused edit must keep what was typed AND stay open: the redirect
    // list and the ceiling are the two fields whose loss is irreversible, so
    // the owner gets the hub's reason beside the values they chose.
    it('reports a refused edit and keeps what was typed', async () => {
      mockUpdate.mockRejectedValue(new Error('the hub refused'))
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
      const form = await screen.findByTestId('edit-app-form-app-1')
      fireEvent.input(within(form).getByLabelText(/Name/), { target: { value: 'Renamed' } })
      fireEvent.click(within(form).getByRole('button', { name: 'Save' }))

      expect(await screen.findByText(/the hub refused/)).toBeInTheDocument()
      expect(within(form).getByLabelText(/Name/)).toHaveValue('Renamed')
      expect(within(form).getByLabelText(/Redirect addresses/)).toHaveValue('https://example.com/callback')
    })
  })

  describe('the step-up allowance', () => {
    // Allowing multiplies what the app's grant reaches, so it asks first.
    it('asks before allowing and then records it', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Allow step-up' }))
      const dialog = await screen.findByRole('dialog', { name: 'Allow the step-up stage?' })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Allow' }))

      await waitFor(() => expect(mockSetElevation).toHaveBeenCalledWith({ clientId: 'app-1', allowed: true }))
    })

    // Refusing only reduces access, so it takes no dialog -- one click.
    it('refuses without a dialog and reports that live windows close', async () => {
      mockList.mockResolvedValue({ apps: [{ ...app, elevationAllowed: true }] })
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')
      expect(screen.getByText('step-up allowed')).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: 'Refuse step-up' }))

      await waitFor(() => expect(mockSetElevation).toHaveBeenCalledWith({ clientId: 'app-1', allowed: false }))
      expect(await screen.findByText(/live elevation window closes/)).toBeInTheDocument()
    })

    // The allowance is the ONE field a built-in registration may still
    // change, so the control is offered on the built-in row too.
    it('is offered on a built-in registration', async () => {
      mockList.mockResolvedValue({
        apps: [{ ...app, registrationSource: 'builtin', clientName: 'LeapMux control CLI' }],
      })
      render(() => <AppRegistrations />)
      await screen.findByText('LeapMux control CLI')

      expect(screen.getByRole('button', { name: 'Allow step-up' })).toBeEnabled()
    })
  })

  describe('the vouch', () => {
    // Only an administrator may vouch: the listing already hides everybody
    // else's private apps from a non-admin, and self-vouching is exactly the
    // thing a vouch is not.
    it('offers no vouch control to an ordinary account', async () => {
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')
      expect(screen.queryByRole('button', { name: 'Vouch' })).not.toBeInTheDocument()
    })

    it('records an administrator\'s vouch', async () => {
      authState.admin = true
      render(() => <AppRegistrations />)
      await screen.findByText('My integration')

      fireEvent.click(screen.getByRole('button', { name: 'Vouch' }))

      await waitFor(() => expect(mockVerify).toHaveBeenCalledWith({ clientId: 'app-1', verified: true }))
    })

    it('withdraws an existing vouch', async () => {
      authState.admin = true
      mockList.mockResolvedValue({
        apps: [{ ...app, verified: true, verifiedAt: { seconds: 1767225600n, nanos: 0 }, verifiedByUsername: 'ada' }],
      })
      render(() => <AppRegistrations />)
      await screen.findByText('verified by ada')

      fireEvent.click(screen.getByRole('button', { name: 'Withdraw vouch' }))

      await waitFor(() => expect(mockVerify).toHaveBeenCalledWith({ clientId: 'app-1', verified: false }))
    })
  })

  // The ADMINISTRATION twin: the same editor wearing variant="hub-wide", the
  // panel the Hub-wide Apps section renders. Its two differences are pinned
  // here -- the registration it writes is HUB_WIDE, and the listing it asks
  // for is the hub's own catalogue alone.
  describe('the hub-wide variant', () => {
    it('registers a hub-wide app', async () => {
      render(() => <AppRegistrations variant="hub-wide" />)
      await screen.findByTestId('hub-wide-app-registrations')

      fireEvent.click(screen.getByRole('button', { name: 'Register a hub-wide app' }))
      fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'Deploy bot' } })
      fireEvent.click(screen.getByLabelText('workspace:read'))
      fireEvent.click(screen.getByRole('button', { name: 'Register' }))

      await waitFor(() => expect(mockRegister).toHaveBeenCalled())
      const sent = mockRegister.mock.calls[0]![0] as { visibility: number, scopes: number[] }
      // HUB_WIDE, the visibility only an administrator may send -- the row
      // that renders this variant exists for administrators alone.
      expect(sent.visibility).toBe(AppVisibility.HUB_WIDE)
      expect(sent.scopes).toEqual([Scope.WORKSPACE_READ])
    })

    // The hub's own catalogue, not the administrator's second Apps list: the
    // narrowing rides the REQUEST, so an administrator's private
    // registrations never cross the wire to a panel that cannot draw them.
    it('asks the hub for the hub-wide reach alone', async () => {
      mockList.mockResolvedValue({
        apps: [{ ...app, clientId: 'app-hub', clientName: 'Hub tool', visibility: 2 }],
      })
      render(() => <AppRegistrations variant="hub-wide" />)

      expect(await screen.findByText('Hub tool')).toBeInTheDocument()
      const sent = mockList.mock.calls[0]![0] as { visibility?: number }
      expect(sent.visibility).toBe(AppVisibility.HUB_WIDE)
      // The reach badge is the section's own title restated per row, so the
      // variant does not draw it.
      expect(screen.queryByText('hub-wide')).not.toBeInTheDocument()
    })

    it('asks for its whole editable set on the user panel', async () => {
      mockList.mockResolvedValue({ apps: [app] })
      render(() => <AppRegistrations />)

      expect(await screen.findByTestId('app-registration-app-1')).toBeInTheDocument()
      const sent = mockList.mock.calls[0]![0] as { visibility?: number }
      expect(sent.visibility).toBeUndefined()
    })

    it('says so when the hub holds none', async () => {
      mockList.mockResolvedValue({ apps: [] })
      render(() => <AppRegistrations variant="hub-wide" />)
      expect(await screen.findByText(/No hub-wide app registrations/)).toBeInTheDocument()
    })
  })
})

// Two refreshes can overlap (the onMount load and one a write path fires),
// and the one that started EARLIER can finish last -- adopting its pages
// would drop a row the newer refresh already shows. Only the newest
// generation may write its result.
describe('refresh generation guard', () => {
  it('a late-finishing older refresh does not clobber a row a write added', async () => {
    const newerApp = { ...app, clientId: 'app-new', clientName: 'Newer registration' }
    // onMount refresh: page one answers, page TWO stalls -- the older
    // generation is parked mid-pagination.
    const stalePageTwo = deferred<{ apps: typeof app[], nextCursor: string, openRegistrationEnabled: boolean }>()
    mockList
      .mockResolvedValueOnce({ apps: [app], nextCursor: 'cursor-2', openRegistrationEnabled: false })
      .mockReturnValueOnce(stalePageTwo.promise)
      // A write path then fires a NEWER refresh, which answers in one page.
      .mockResolvedValueOnce({ apps: [newerApp], nextCursor: '', openRegistrationEnabled: false })
    render(() => <AppRegistrations />)

    // The register form's success handler PREPENDS the row the response
    // carries, with no re-page -- so the new registration shows while the
    // older refresh is still parked, and must SURVIVE that refresh landing.
    mockRegister.mockResolvedValue({ app: newerApp, clientSecret: '' })
    fireEvent.click(screen.getByRole('button', { name: 'Register an app' }))
    fireEvent.input(screen.getByLabelText(/Name/), { target: { value: 'CI bot' } })
    fireEvent.click(screen.getByLabelText('workspace:read'))
    fireEvent.click(screen.getByRole('button', { name: 'Register' }))
    await waitFor(() => expect(mockRegister).toHaveBeenCalled())

    // The NEWER refresh lands while the older one is still parked.
    await waitFor(() => expect(screen.getByText('Newer registration')).toBeInTheDocument())

    // The OLDER refresh's parked page resolves now, carrying the stale list.
    stalePageTwo.resolve({ apps: [app], nextCursor: '', openRegistrationEnabled: false })
    await new Promise(resolve => setTimeout(resolve, 20))

    expect(screen.getByText('Newer registration')).toBeInTheDocument()
  })
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}
