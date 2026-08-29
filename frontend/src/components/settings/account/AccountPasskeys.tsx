import type { Component } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import type { PasskeyInfo } from '~/generated/leapmux/v1/user_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createSignal, For, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Alert } from '~/components/common/Alert'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { Dialog } from '~/components/common/Dialog'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { Spinner } from '~/components/common/Spinner'
import { StatusLine } from '~/components/common/StatusLine'
import { Tooltip } from '~/components/common/Tooltip'
import { useAuth } from '~/context/AuthContext'
import { elevationPrompting } from '~/lib/elevationPrompt'
import { formatErrorMessage } from '~/lib/errors'
import {
  credentialIdFromRegistrationJson,
  loadPasskeys,
} from '~/lib/passkeyManagement'
import { passkeyBlocker } from '~/lib/systemInfo'
import { passkeyBlockerMessage, passkeyErrorMessage, signalAcceptedPasskeys, signalPasskeyRemoved, startRegistration } from '~/lib/webauthn'
import { warningText } from '~/styles/shared.css'
import * as styles from './accountFields.css'
import * as listStyles from './credentialList.css'

type ModalMode = 'add' | 'delete' | 'deactivate' | null

export const AccountPasskeys: Component = () => {
  const auth = useAuth()
  // Every mutation here is sensitive, and NONE of them checks that first: the
  // hub refuses an un-elevated session on its own, and the transport turns
  // that refusal into one prompt and one retry of the refused request. See
  // the elevationInterceptor in `~/api/transport` for why attempt-then-prompt
  // is the ordering, and `elevationPrompting` below for the one thing this
  // component still reads from it.
  const [passkeys, setPasskeys] = createSignal<PasskeyInfo[]>([])
  // The hub's relying party ID for Signal API calls; arrives with the
  // passkey list so this component never derives it locally.
  const [rpId, setRpId] = createSignal('')
  const [loading, setLoading] = createSignal(true)
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)
  const [modalMessage, setModalMessage] = createSignal<StatusMessage | null>(null)
  const [modal, setModal] = createSignal<ModalMode>(null)
  const [targetPasskey, setTargetPasskey] = createSignal<PasskeyInfo | null>(null)
  const [newPassword, setNewPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [friendlyName, setFriendlyName] = createSignal('')
  const [renamingId, setRenamingId] = createSignal<string | null>(null)
  const [renameValue, setRenameValue] = createSignal('')

  /**
   * Why this page cannot add a passkey, in the words the reader needs, or
   * undefined when it can.
   *
   * TWO parties can refuse, and the button says which BEFORE the click. The
   * hub refuses an origin it does not publish -- reach the same hub by
   * another address (the LAN IP behind the reverse proxy, a tunnel host, a
   * port that the public URL does not specify) and every Begin answers "origin
   * is not allowed for passkey ceremonies". The browser refuses a page that is
   * not a secure context, and it refuses by exposing no WebAuthn API at all: the
   * ceremony then failed with "WebAuthn is not supported in this browser",
   * which reads as a broken browser rather than a plain-HTTP address.
   */
  const blockedReason = () => {
    const blocker = passkeyBlocker()
    return blocker ? passkeyBlockerMessage(blocker) : undefined
  }

  const passwordSet = () => auth.user()?.passwordSet === true
  const passkeyCount = () => passkeys().length
  // Working means "do not start another mutation": either this component is
  // mid-request, or a step-up prompt is open for one of its requests. ONE
  // prompt serves the whole app, so a second action started underneath it
  // would queue behind the same dialog.
  const working = () => busy() || elevationPrompting()

  const resetModalFields = () => {
    setNewPassword('')
    setConfirmPassword('')
    setFriendlyName('')
    setTargetPasskey(null)
  }

  /**
   * Adopt a new local list. Nothing else: no round trip.
   *
   * Use it for a write that cannot change what OTHER surfaces read -- a
   * rename, and the initial load, whose count the bootstrap already carried.
   */
  const setLocalList = (list: PasskeyInfo[]) => setPasskeys(list)

  /**
   * Adopt a new local list AND re-read the cached account.
   *
   * The account's passkey COUNT and its `passwordSet` flag live on
   * auth.user(), and other surfaces read them -- the step-up prompt offers
   * the passkey factor only when the account has one. Refreshing here keeps
   * that single source of truth current, rather than threading a second
   * count through a prop that can disagree with it.
   *
   * AWAITED, and the caller waits on the result. A fire-and-forget refresh
   * let a deactivation resolve before the cached user landed, so the Password
   * button still read "Set Password" for an account that just set one,
   * and a prompt opened in that window offered a passkey factor for passkeys
   * that no longer existed.
   */
  const applyLocalList = async (list: PasskeyInfo[]) => {
    setLocalList(list)
    await auth.refreshUser()
  }

  const forceCloseModal = () => {
    setModal(null)
    setModalMessage(null)
    resetModalFields()
  }

  const refresh = async (opts?: { preserveSuccess?: boolean }) => {
    const preserveSuccess = opts?.preserveSuccess === true && message()?.type === 'success'
    setLoading(true)
    try {
      const list = await loadPasskeys()
      setRpId(list.rpId)
      // setLocalList, not applyLocalList: a READ cannot have changed the
      // count, and the bootstrap already carried it. Refreshing here spent a
      // GetCurrentUser round trip every time the panel opened.
      setLocalList(list.passkeys)
    }
    catch (e) {
      if (!preserveSuccess)
        setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to load passkeys') })
    }
    finally {
      setLoading(false)
    }
  }

  onMount(() => {
    void refresh()
  })

  // A busy dialog is not dismissible; everything else is forceCloseModal.
  const closeModal = () => {
    if (working())
      return
    forceCloseModal()
  }

  /**
   * The three dialogs open on the CLICK. None of them verifies anything first.
   *
   * Attempt-then-prompt, the same as every other surface: the hub refuses an
   * un-elevated session, the transport turns that refusal into one prompt and
   * one retry, and `ElevationPromptHost` puts that prompt above whichever
   * dialog raised it and holds that dialog inert until it settles. This panel
   * used to pre-empt the stack per click, which decided no authorization -- the
   * interceptor runs either way -- and left the next dialog to raise a
   * restricted call to copy the same reasoning.
   */
  const openAdd = () => {
    resetModalFields()
    setModalMessage(null)
    setFriendlyName('Passkey')
    setModal('add')
  }

  const openDelete = (passkey: PasskeyInfo) => {
    resetModalFields()
    setTargetPasskey(passkey)
    setModal('delete')
  }

  const openDeactivate = () => {
    resetModalFields()
    setModal('deactivate')
  }

  // Removing the LAST passkey from an account that has no password would
  // leave nothing to sign in with, so that one case demands a replacement.
  const deleteIsDeactivation = () => !passwordSet() && passkeyCount() === 1
  // Through passwordCanSubmit, the same predicate every other password form
  // uses, so these two dialogs run validatePassword with live feedback
  // instead of an equality check. The hub validates in `prepare`, which runs
  // AFTER the elevation ceremony -- so a password it will refuse used to
  // surface only once the user already answered a biometric prompt.
  const newPasswordReady = () => passwordCanSubmit({ password: newPassword, confirmPassword })
  const deleteConfirmDisabled = () => working() || (deleteIsDeactivation() && !newPasswordReady())
  const deactivateConfirmDisabled = () => working() || (!passwordSet() && !newPasswordReady())

  const modalError = (fallback: string) => (e: unknown) => {
    const text = passkeyErrorMessage(e, fallback)
    if (text)
      setModalMessage({ type: 'error', text })
  }

  const handleAddPasskey = async () => {
    setBusy(true)
    setModalMessage(null)
    try {
      const begin = await userClient.beginPasskeyRegistration({})
      const credentialJson = await startRegistration(begin.optionsJson)
      const finish = await userClient.finishPasskeyRegistration({
        sessionId: begin.sessionId,
        credentialJson,
        friendlyName: friendlyName().trim() || 'Passkey',
      })
      forceCloseModal()
      setMessage({ type: 'success', text: 'Passkey added.' })
      // The optimistic row goes in only when the Finish echoed one.
      if (finish.passkey) {
        const added = finish.passkey
        setLocalList([...passkeys().filter(pk => pk.id !== added.id), added])
      }
      // The cached account follows EVERY successful Finish, so this sits
      // outside that branch. The success message is unconditional, and the
      // cached passkey COUNT is what ElevateForm reads to decide whether to
      // offer the passkey factor -- a Finish that echoes no row would leave the
      // count one short and hide a factor the account holds.
      await auth.refreshUser()
      const userId = auth.user()?.id
      if (userId && rpId()) {
        const ids = new Set(passkeys().map(pk => pk.credentialId).filter(Boolean))
        const fromFinish = finish.passkey?.credentialId
        if (fromFinish)
          ids.add(fromFinish)
        const fromJson = credentialIdFromRegistrationJson(credentialJson)
        if (fromJson)
          ids.add(fromJson)
        signalAcceptedPasskeys(rpId(), userId, [...ids])
      }
    }
    catch (e) {
      // The transport retries a refused request ONCE, on its own, after the
      // step-up prompt. So a refusal that reaches here is either a second
      // refusal or a failure the prompt cannot fix, and both are the user's
      // to read.
      modalError('Failed to add passkey')(e)
    }
    finally {
      setBusy(false)
    }
  }

  const handleDeletePasskey = async () => {
    const passkey = targetPasskey()
    if (!passkey)
      return
    if (deleteIsDeactivation() && !newPasswordReady()) {
      setModalMessage({ type: 'error', text: 'Set a new password to remove your last passkey.' })
      return
    }
    setBusy(true)
    setModalMessage(null)
    try {
      await userClient.deletePasskey({
        id: passkey.id,
        newPassword: deleteIsDeactivation() ? newPassword() : '',
      })
      if (rpId())
        signalPasskeyRemoved(rpId(), passkey.credentialId)
      const wasDeactivation = deleteIsDeactivation()
      forceCloseModal()
      setMessage({ type: 'success', text: 'Passkey removed.' })
      await applyLocalList(passkeys().filter(pk => pk.id !== passkey.id))
      // Only the last-passkey deactivation flips passwordSet; a plain
      // delete changed nothing the local list does not already carry.
      if (wasDeactivation)
        await refresh({ preserveSuccess: true })
    }
    catch (e) {
      modalError('Failed to remove passkey')(e)
    }
    finally {
      setBusy(false)
    }
  }

  const handleDeactivate = async () => {
    if (!passwordSet() && !newPasswordReady()) {
      setModalMessage({ type: 'error', text: 'Set a new password to disable passkey sign-in.' })
      return
    }
    setBusy(true)
    setModalMessage(null)
    try {
      await userClient.deactivatePasskeyAuth({
        newPassword: passwordSet() ? '' : newPassword(),
      })
      if (rpId()) {
        for (const pk of passkeys())
          signalPasskeyRemoved(rpId(), pk.credentialId)
      }
      forceCloseModal()
      setMessage({ type: 'success', text: 'Passkey sign-in disabled.' })
      await applyLocalList([])
      // Deactivation always flips passwordSet on a passkey-only account.
      await refresh({ preserveSuccess: true })
    }
    catch (e) {
      modalError('Failed to deactivate passkeys')(e)
    }
    finally {
      setBusy(false)
    }
  }

  const startRename = (passkey: PasskeyInfo) => {
    setRenamingId(passkey.id)
    setRenameValue(passkey.friendlyName || 'Passkey')
  }

  const cancelRename = () => {
    setRenamingId(null)
    setRenameValue('')
  }

  const saveRename = async (id: string) => {
    setBusy(true)
    setMessage(null)
    try {
      const resp = await userClient.renamePasskey({
        id,
        friendlyName: renameValue().trim() || 'Passkey',
      })
      cancelRename()
      setMessage({ type: 'success', text: 'Passkey renamed.' })
      // The response carries the renamed row; a rename changes nothing
      // else -- not the count, not passwordSet -- so it writes the local
      // list alone.
      if (resp.passkey)
        setLocalList(passkeys().map(pk => (pk.id === resp.passkey!.id ? resp.passkey! : pk)))
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to rename passkey') })
    }
    finally {
      setBusy(false)
    }
  }

  const formatPasskeyDate = (passkey: PasskeyInfo) => {
    const ts = passkey.lastUsedAt ?? passkey.createdAt
    if (!ts)
      return ''
    return timestampDate(ts).toLocaleString()
  }

  return (
    <>
      <div class="vstack gap-4">
        <Show when={loading()}>
          <div class={listStyles.credentialListLoading}><Spinner /></div>
        </Show>
        <Show when={!loading() && passkeys().length === 0}>
          <p class={listStyles.credentialListEmpty}>No passkeys registered yet.</p>
        </Show>
        <Show when={blockedReason()}>
          {reason => <Alert variant="warning">{reason()}</Alert>}
        </Show>
        <For each={passkeys()}>
          {passkey => (
            <div class={listStyles.credentialRow}>
              <div class={listStyles.credentialInfo}>
                <Show
                  when={renamingId() !== passkey.id}
                  fallback={(
                    <input
                      type="text"
                      value={renameValue()}
                      onInput={e => setRenameValue(e.currentTarget.value)}
                      onKeyDown={(e) => {
                        // The SAME guard the Save button carries. Without it
                        // a second Enter during the in-flight rename issues a
                        // second rename, which races the first for the same
                        // row and reports whichever refusal lands last.
                        if (e.key === 'Enter' && !working())
                          void saveRename(passkey.id)
                        if (e.key === 'Escape')
                          cancelRename()
                      }}
                    />
                  )}
                >
                  <span class={listStyles.credentialName}>{passkey.friendlyName || 'Passkey'}</span>
                </Show>
                <span class={listStyles.credentialMeta}>
                  {passkey.lastUsedAt ? `Last used ${formatPasskeyDate(passkey)}` : `Added ${formatPasskeyDate(passkey)}`}
                </span>
              </div>
              <div class={actionsFooter}>
                <Show when={renamingId() === passkey.id}>
                  <button type="button" onClick={() => void saveRename(passkey.id)} disabled={working()}>
                    Save
                  </button>
                  <button type="button" onClick={cancelRename} disabled={working()}>Cancel</button>
                </Show>
                <Show when={renamingId() !== passkey.id}>
                  <button type="button" onClick={() => startRename(passkey)} disabled={working()}>Rename</button>
                  {/*
                    An Oat danger OUTLINE: this control only OPENS the removal
                    dialog, whose primary carries the danger weight.
                  */}
                  <button type="button" class="outline" data-variant="danger" onClick={() => openDelete(passkey)} disabled={working()}>
                    Remove
                  </button>
                </Show>
              </div>
            </div>
          )}
        </For>
        <StatusLine message={message()} />
        {/*
          The reason goes through <Tooltip>, which works on a disabled control
          and leaves the button its own name. A `title` this long BECOMES the
          accessible name, so a screen reader announced three sentences of
          remedy where "Add passkey" belongs. The alert above carries the same
          reason for anybody who never hovers.
        */}
        <div class={actionsFooter}>
          <Tooltip text={blockedReason()}>
            <button
              type="button"
              onClick={openAdd}
              disabled={working() || loading() || blockedReason() !== undefined}
            >
              Add passkey
            </button>
          </Tooltip>
          <Show when={passkeyCount() > 0}>
            <button type="button" onClick={openDeactivate} disabled={working() || loading()}>
              Disable passkey sign-in
            </button>
          </Show>
        </div>
      </div>

      <Show when={modal() === 'add'}>
        <Dialog title="Add passkey" onClose={closeModal} busy={working()}>
          <div class="vstack gap-4">
            <label class={styles.fieldLabel}>
              Name
              <input type="text" value={friendlyName()} onInput={e => setFriendlyName(e.currentTarget.value)} />
            </label>
            <StatusLine message={modalMessage()} />
            <div class={actionsFooter}>
              <button type="button" onClick={() => void handleAddPasskey()} disabled={working()}>
                {busy() ? 'Adding…' : 'Continue'}
              </button>
            </div>
          </div>
        </Dialog>
      </Show>

      <Show when={modal() === 'delete'}>
        <Dialog title="Remove passkey" onClose={closeModal} busy={working()}>
          <div class="vstack gap-4">
            <Show when={deleteIsDeactivation()}>
              <div class={warningText}>
                This is your only sign-in method. Removing it requires setting a password.
              </div>
              <PasswordFields
                password={newPassword}
                setPassword={setNewPassword}
                confirmPassword={confirmPassword}
                setConfirmPassword={setConfirmPassword}
                labelClass={styles.fieldLabel}
              />
            </Show>
            <StatusLine message={modalMessage()} />
            {/*
              The danger primary of the dialog it lives in, so it wears the
              same two-click arming every ConfirmDialog danger primary does --
              this dialog is a raw Dialog only because of the password fields
              above, and its ending should not read quieter for that.
              Right-aligned like an Oat dialog footer's primary.
            */}
            <div class={actionsFooter}>
              <ConfirmButton
                data-variant="danger"
                onClick={() => void handleDeletePasskey()}
                disabled={deleteConfirmDisabled()}
              >
                {busy() ? 'Removing…' : 'Remove passkey'}
              </ConfirmButton>
            </div>
          </div>
        </Dialog>
      </Show>

      <Show when={modal() === 'deactivate'}>
        <Dialog title="Disable passkey sign-in" onClose={closeModal} busy={working()}>
          <div class="vstack gap-4">
            <p>Removes all passkeys from your account.</p>
            <Show when={!passwordSet()}>
              <PasswordFields
                password={newPassword}
                setPassword={setNewPassword}
                confirmPassword={confirmPassword}
                setConfirmPassword={setConfirmPassword}
                labelClass={styles.fieldLabel}
              />
            </Show>
            <StatusLine message={modalMessage()} />
            <div class={actionsFooter}>
              <button type="button" onClick={() => void handleDeactivate()} disabled={deactivateConfirmDisabled()}>
                {busy() ? 'Disabling…' : 'Disable passkey sign-in'}
              </button>
            </div>
          </div>
        </Dialog>
      </Show>

    </>
  )
}
