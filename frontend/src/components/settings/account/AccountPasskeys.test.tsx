import { Code, ConnectError } from '@connectrpc/connect'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { promptForElevation, setElevationPrompter } from '~/lib/elevationPrompt'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'
import { AccountPasskeys } from './AccountPasskeys'

const mockListPasskeys = vi.fn()
const mockDeletePasskey = vi.fn()
const mockDeactivatePasskeyAuth = vi.fn()
const mockRenamePasskey = vi.fn()
const mockChangePassword = vi.fn()
const mockSignalPasskeyRemoved = vi.fn()
const mockSignalAcceptedPasskeys = vi.fn()
const mockBeginPasskeyRegistration = vi.fn()
const mockFinishPasskeyRegistration = vi.fn()
const mockStartRegistration = vi.fn()
const mockUser = vi.fn()
const mockRefreshUser = vi.fn()

vi.mock('~/api/clients', () => ({
  userClient: {
    listPasskeys: (...args: unknown[]) => mockListPasskeys(...args),
    changePassword: (...args: unknown[]) => mockChangePassword(...args),
    renamePasskey: (...args: unknown[]) => mockRenamePasskey(...args),
    deletePasskey: (...args: unknown[]) => mockDeletePasskey(...args),
    deactivatePasskeyAuth: (...args: unknown[]) => mockDeactivatePasskeyAuth(...args),
    beginPasskeyRegistration: (...args: unknown[]) => mockBeginPasskeyRegistration(...args),
    finishPasskeyRegistration: (...args: unknown[]) => mockFinishPasskeyRegistration(...args),
  },
}))

// No elevation member: this panel reads NO deadline. It attempts every
// mutation, and the hub, the transport and `ElevationPromptHost` between them
// supply the refusal, the prompt and the retry.
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    refreshUser: mockRefreshUser,
  }),
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

// This file stubs the ceremony and the Signal API; it does NOT stub
// `passkeyBlockerMessage`. It is a pure map from a blocker to the sentence the
// panel shows, so the real one is what makes the assertions below read the
// text a user reads.
vi.mock('~/lib/webauthn', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/webauthn')>(),
  startRegistration: (...args: unknown[]) => mockStartRegistration(...args),
  startAuthentication: vi.fn(),
  passkeyErrorMessage: (_e: unknown, fallback: string) => fallback,
  signalPasskeyRemoved: (...args: unknown[]) => mockSignalPasskeyRemoved(...args),
  signalAcceptedPasskeys: (...args: unknown[]) => mockSignalAcceptedPasskeys(...args),
}))

vi.mock('~/lib/passkeyManagement', async () => {
  const actual = await vi.importActual<typeof import('~/lib/passkeyManagement')>('~/lib/passkeyManagement')
  return {
    ...actual,
    loadPasskeys: async () => {
      const resp = await mockListPasskeys({})
      return { passkeys: resp.passkeys ?? [], rpId: resp.rpId ?? 'localhost' }
    },
  }
})

const passwordUser = {
  id: 'user-1',
  username: 'alice',
  displayName: 'Alice',
  email: 'alice@example.com',
  emailVerified: true,
  passwordSet: true,
  passkeyCount: 1,
  oauthProviders: [],
}

const passkeyOnlyUser = { ...passwordUser, passwordSet: false }

/**
 * The LAST field with this label, which is the one inside the open dialog.
 *
 * The passkey dialogs render `<PasswordFields>`, the same component the
 * account's own password row uses, so both can carry "New Password" and
 * "Confirm Password" on one panel. The test disambiguates by position rather
 * than by a label the dialog would have to keep to itself.
 */
function lastLabelled(label: string): HTMLElement {
  const matches = screen.getAllByLabelText(label)
  return matches[matches.length - 1]!
}

// The PROMPT-AND-RETRY cases do not live here. A refused sensitive action
// opens the step-up prompt from the TRANSPORT, so no call site opts in -- see
// the elevationInterceptor cases in `~/api/transport.test.ts` and
// `~/components/common/ElevationPromptHost.test.tsx`. This file mocks
// `userClient` directly, which bypasses the transport entirely, so a prompt
// assertion here would test a wrapper rather than the rule.
describe('accountPasskeys', () => {
  beforeEach(() => {
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.open = true
    })
    HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
      this.open = false
    })
    vi.clearAllMocks()
    resetSystemInfoMock()
    setElevationPrompter(null)
    mockRefreshUser.mockResolvedValue(undefined)
    mockUser.mockReturnValue(passwordUser)
    mockListPasskeys.mockResolvedValue({
      passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      rpId: 'localhost',
    })
    mockDeletePasskey.mockResolvedValue({})
    mockDeactivatePasskeyAuth.mockResolvedValue({})
    mockRenamePasskey.mockResolvedValue({})
    mockChangePassword.mockResolvedValue({})
    mockBeginPasskeyRegistration.mockResolvedValue({ sessionId: 'sess-1', optionsJson: '{}' })
    mockStartRegistration.mockResolvedValue('{"id":"cred-new"}')
    mockFinishPasskeyRegistration.mockResolvedValue({
      passkey: { id: 'pk-2', friendlyName: 'Phone', transports: [], credentialId: 'cred-new' },
    })
  })

  it('adds a passkey with no secret in the dialog', async () => {
    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    expect(await screen.findByLabelText('Name')).toBeInTheDocument()
    // The step-up is the session's elevation, not a secret re-typed here.
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    await vi.waitFor(() => {
      expect(mockFinishPasskeyRegistration).toHaveBeenCalledWith(expect.objectContaining({
        sessionId: 'sess-1',
        credentialJson: '{"id":"cred-new"}',
      }))
    })
    expect(mockSignalAcceptedPasskeys).toHaveBeenCalledWith(
      'localhost',
      'user-1',
      expect.arrayContaining(['cred-abc', 'cred-new']),
    )
  })

  it('renames a passkey without prompting again', async () => {
    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => {
      expect(mockRenamePasskey).toHaveBeenCalledWith({ id: 'pk-1', friendlyName: 'Laptop' })
    })
    expect(screen.queryByTestId('elevate-password')).not.toBeInTheDocument()
  })

  it('demands a replacement password for the last passkey on a passwordless account', async () => {
    mockUser.mockReturnValue(passkeyOnlyUser)

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(await screen.findByText(/only sign-in method/i)).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Remove passkey' })).toBeDisabled()

    fireEvent.input(lastLabelled('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(lastLabelled('Confirm Password'), { target: { value: 'newpass123' } })
    expect(screen.getByRole('button', { name: 'Remove passkey' })).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Remove passkey' }))
    await vi.waitFor(() => {
      expect(mockDeletePasskey).toHaveBeenCalledWith({ id: 'pk-1', newPassword: 'newpass123' })
    })
  })

  it('deactivates passkey sign-in and signals every removed credential', async () => {
    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Disable passkey sign-in' }))
    const confirmButtons = await screen.findAllByRole('button', { name: 'Disable passkey sign-in' })
    fireEvent.click(confirmButtons[confirmButtons.length - 1]!)
    await vi.waitFor(() => {
      expect(mockDeactivatePasskeyAuth).toHaveBeenCalledWith({ newPassword: '' })
    })
    expect(mockSignalPasskeyRemoved).toHaveBeenCalledWith('localhost', 'cred-abc')
  })

  it('deactivates a passkey-only account only with a replacement password', async () => {
    mockUser.mockReturnValue(passkeyOnlyUser)

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Disable passkey sign-in' }))
    const disabled = await screen.findAllByRole('button', { name: 'Disable passkey sign-in' })
    expect(disabled[disabled.length - 1]!).toBeDisabled()

    fireEvent.input(lastLabelled('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(lastLabelled('Confirm Password'), { target: { value: 'newpass123' } })
    const enabled = screen.getAllByRole('button', { name: 'Disable passkey sign-in' })
    fireEvent.click(enabled[enabled.length - 1]!)
    await vi.waitFor(() => {
      expect(mockDeactivatePasskeyAuth).toHaveBeenCalledWith({ newPassword: 'newpass123' })
    })
  })

  it('reports a refusal a prompt cannot fix instead of prompting', async () => {
    // FailedPrecondition WITHOUT the marker: the account has no password to
    // verify with, and opening a step-up dialog would ask for one.
    mockRenamePasskey.mockRejectedValue(
      new ConnectError('account credentials changed; please retry', Code.FailedPrecondition),
    )

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText(/account credentials changed/)).toBeInTheDocument()
    expect(screen.queryByTestId('elevate-password')).not.toBeInTheDocument()
  })

  it('keeps the added passkey in the list when the follow-up refresh fails', async () => {
    mockListPasskeys
      .mockResolvedValueOnce({
        passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      })
      .mockRejectedValue(new Error('list failed'))

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    expect(await screen.findByText('Phone')).toBeInTheDocument()
    expect(screen.getByText('Laptop')).toBeInTheDocument()
    expect(screen.getByText('Passkey added.')).toBeInTheDocument()
  })

  it('keeps the passkey removed from the list when the follow-up refresh fails', async () => {
    mockListPasskeys
      .mockResolvedValueOnce({
        passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      })
      .mockRejectedValue(new Error('list failed'))

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Remove passkey' }))
    await vi.waitFor(() => {
      expect(screen.queryByText('Laptop')).not.toBeInTheDocument()
    })
    expect(screen.getByText('Passkey removed.')).toBeInTheDocument()
  })

  it('shows the add dialog with only a name field', async () => {
    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    const addDialog = await screen.findByRole('dialog', { name: 'Add passkey' })
    expect(within(addDialog).getByLabelText('Name')).toBeInTheDocument()
    expect(within(addDialog).queryByTestId('elevate-passkey')).not.toBeInTheDocument()
  })

  // The passkey COUNT and passwordSet live on auth.user(), and other surfaces
  // read them -- the step-up prompt offers the passkey factor only when the
  // account has one. A write that moves either must re-read the cached
  // account, and a write that moves neither must not spend a round trip doing
  // it. Neither half was pinned, so a future edit could drop the refresh
  // (leaving the prompt offering a factor that no longer exists) or add one to
  // every keystroke, and the suite would stay green.
  it('re-reads the cached account after a write that changes the count', async () => {
    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    // Opening the panel is a READ: the bootstrap already carried the count.
    expect(mockRefreshUser).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Remove passkey' }))

    await vi.waitFor(() => {
      expect(mockRefreshUser).toHaveBeenCalled()
    })
  })

  // EVERY successful Finish, whether or not it echoed a row. The success
  // message is unconditional, and the cached count is what ElevateForm reads to
  // decide whether to offer the passkey factor -- so a refresh that sat inside
  // `if (finish.passkey)` left an account one passkey short of its own truth
  // and hid a factor it holds.
  it('re-reads the cached account when the Finish echoes no passkey', async () => {
    mockFinishPasskeyRegistration.mockResolvedValue({})

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    expect(mockRefreshUser).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))

    expect(await screen.findByText('Passkey added.')).toBeInTheDocument()
    expect(mockRefreshUser).toHaveBeenCalledTimes(1)
  })

  it('does not re-read the cached account after a rename', async () => {
    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await vi.waitFor(() => {
      expect(mockRenamePasskey).toHaveBeenCalled()
    })
    // A rename moves neither the count nor passwordSet.
    expect(mockRefreshUser).not.toHaveBeenCalled()
  })
})

/**
 * The reason a disabled control carries, read the way a screen reader gets it.
 *
 * <Tooltip> leaves an offscreen description in `aria-describedby` for as long
 * as the control is disabled. It is NOT `title`: a reason this long on `title`
 * becomes the control's accessible name, which is the defect these tests pin.
 */
function reasonOf(el: Element): string {
  const describedBy = el.getAttribute('aria-describedby')
  expect(describedBy).toBeTruthy()
  return document.getElementById(describedBy!)?.textContent ?? ''
}

/**
 * A ceremony runs against the ORIGIN the browser is on, and the hub accepts
 * only the origins it publishes. Reach the same hub by another address and
 * every Begin answers "origin is not allowed for passkey ceremonies" -- which
 * used to arrive AFTER the click, worded for a server log, and (once the
 * session was already elevated) as the only thing the dialog ever said.
 */
describe('accountPasskeys on a page that cannot run a ceremony', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setElevationPrompter(null)
    mockRefreshUser.mockResolvedValue(undefined)
    mockUser.mockReturnValue(passwordUser)
    mockListPasskeys.mockResolvedValue({ passkeys: [], rpId: 'localhost' })
  })

  it('disables Add passkey and says why', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    render(() => <AccountPasskeys />)

    expect(await screen.findByText('No passkeys registered yet.')).toBeInTheDocument()
    const add = screen.getByRole('button', { name: 'Add passkey' })
    expect(add).toBeDisabled()
    expect(reasonOf(add)).toMatch(/does not run passkey ceremonies at this address/i)
    expect(screen.getByRole('alert').textContent).toMatch(/configured URL/i)
  })

  /**
   * The button keeps its own name while it carries the reason.
   *
   * The reason is three sentences, and a `title` alone became the whole
   * accessible NAME of the button: a screen reader announced the remedy in
   * place of "Add passkey", and every by-name lookup stopped matching. jsdom's
   * name computation prefers the contents, so only a real browser showed it --
   * which is why the E2E spec that switches sections is the other half of this
   * cover.
   */
  it('keeps its own accessible name while it carries the reason', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    render(() => <AccountPasskeys />)

    expect(await screen.findByText('No passkeys registered yet.')).toBeInTheDocument()
    // The lookup itself is the assertion: the name is the LABEL. With the
    // reason on `title` it became the name, and this found nothing.
    const add = screen.getByRole('button', { name: 'Add passkey' })
    expect(add).not.toHaveAttribute('title')
    expect(add).not.toHaveAttribute('aria-label')
    expect(reasonOf(add)).toMatch(/does not run passkey ceremonies at this address/i)
  })

  it('leaves Add passkey working on an origin the hub serves', async () => {
    render(() => <AccountPasskeys />)

    // The listing disables the button too, so wait for it before reading the
    // origin's own answer -- otherwise this passes on the loading state.
    expect(await screen.findByText('No passkeys registered yet.')).toBeInTheDocument()
    const add = screen.getByRole('button', { name: 'Add passkey' })
    expect(add).toBeEnabled()
    expect(add.getAttribute('title')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  /**
   * The blocker the BROWSER raises, and the reason this test exists at all.
   *
   * A hub reached at its own published plain-HTTP address answers
   * passkey_enabled = true, so the panel used to enable Add passkey and the
   * ceremony died inside @simplewebauthn/browser with "WebAuthn is not
   * supported in this browser" -- which specifies the browser, when the
   * browser is fine and the PAGE is not secure. The remedy must not specify an
   * administrator either: no address anybody publishes makes a plain-HTTP
   * page secure.
   */
  it('specifies the insecure page, not the browser and not the hub', async () => {
    setSystemInfoMock({ passkeyBlocker: 'insecure-context' })
    render(() => <AccountPasskeys />)

    expect(await screen.findByText('No passkeys registered yet.')).toBeInTheDocument()
    const add = screen.getByRole('button', { name: 'Add passkey' })
    expect(add).toBeDisabled()
    expect(reasonOf(add)).toMatch(/secure page/i)
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toMatch(/Open the hub over HTTPS/i)
    expect(alert.textContent).not.toMatch(/administrator/i)
  })

  it('specifies the browser when the page is secure and WebAuthn is absent', async () => {
    setSystemInfoMock({ passkeyBlocker: 'no-webauthn' })
    render(() => <AccountPasskeys />)

    expect(await screen.findByText('No passkeys registered yet.')).toBeInTheDocument()
    const add = screen.getByRole('button', { name: 'Add passkey' })
    expect(add).toBeDisabled()
    expect(screen.getByRole('alert').textContent).toMatch(/browser does not support passkeys/i)
  })

  // Removing a passkey runs no ceremony -- it is a plain RPC -- so the origin
  // must not take away the only control that can clean up after one.
  it('still offers removal', async () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    mockListPasskeys.mockResolvedValue({
      passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      rpId: 'localhost',
    })
    render(() => <AccountPasskeys />)

    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Disable passkey sign-in' })).toBeEnabled()
  })
})

/**
 * The dialogs open on the CLICK. Nothing here verifies anything first.
 *
 * Attempt-then-prompt, the same as every other surface. This panel used to
 * pre-empt the modal stack per click -- it read the mirrored elevation
 * deadline and opened the step-up prompt BEFORE its own dialog, so a refusal
 * raised from inside one would not land as a third modal. That decided no
 * authorization (the transport's interceptor runs on the request either way),
 * and it left the next dialog with a restricted call to copy the same
 * reasoning. `ElevationPromptHost` owns the stack now.
 */
describe('accountPasskeys opens its dialogs on the click', () => {
  beforeEach(() => {
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.open = true
    })
    HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
      this.open = false
    })
    vi.clearAllMocks()
    resetSystemInfoMock()
    mockRefreshUser.mockResolvedValue(undefined)
    mockUser.mockReturnValue(passwordUser)
    mockListPasskeys.mockResolvedValue({
      passkeys: [{ id: 'pk-1', friendlyName: 'Laptop', transports: [], credentialId: 'cred-abc' }],
      rpId: 'localhost',
    })
    mockBeginPasskeyRegistration.mockResolvedValue({ sessionId: 'sess-1', optionsJson: '{}' })
    mockStartRegistration.mockResolvedValue('{"id":"cred-new"}')
    mockFinishPasskeyRegistration.mockResolvedValue({
      passkey: { id: 'pk-2', friendlyName: 'Phone', transports: [], credentialId: 'cred-new' },
    })
    mockDeletePasskey.mockResolvedValue({})
    mockDeactivatePasskeyAuth.mockResolvedValue({})
  })

  afterEach(() => setElevationPrompter(null))

  it('opens the add dialog without verifying anything first', async () => {
    const prompt = vi.fn(async () => true)
    setElevationPrompter(prompt)

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add passkey' }))

    expect(await screen.findByRole('dialog', { name: 'Add passkey' })).toBeInTheDocument()
    expect(prompt).not.toHaveBeenCalled()
    // The ceremony starts on Continue, so the click that opens the dialog
    // asks the hub for nothing.
    expect(mockBeginPasskeyRegistration).not.toHaveBeenCalled()
  })

  it('opens the remove and the disable dialogs on the click too', async () => {
    const prompt = vi.fn(async () => true)
    setElevationPrompter(prompt)

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(await screen.findByRole('dialog', { name: 'Remove passkey' })).toBeInTheDocument()

    fireEvent.click(within(screen.getByRole('dialog', { name: 'Remove passkey' })).getByRole('button', { name: /close/i }))
    await vi.waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Remove passkey' })).not.toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Disable passkey sign-in' }))
    expect(await screen.findByRole('dialog', { name: 'Disable passkey sign-in' })).toBeInTheDocument()

    expect(prompt).not.toHaveBeenCalled()
  })

  // The ONE thing this panel still reads from the prompt. One prompt serves
  // the whole app, so a second action started underneath it would queue behind
  // the same dialog.
  it('disables its controls while a prompt is open, and enables them again', async () => {
    let release!: (proven: boolean) => void
    setElevationPrompter(() => new Promise<boolean>((done) => {
      release = done
    }))

    render(() => <AccountPasskeys />)
    expect(await screen.findByText('Laptop')).toBeInTheDocument()
    const add = screen.getByRole('button', { name: 'Add passkey' })
    expect(add).not.toBeDisabled()

    void promptForElevation()
    await vi.waitFor(() => expect(add).toBeDisabled())

    release(false)
    await vi.waitFor(() => expect(add).not.toBeDisabled())
  })
})
