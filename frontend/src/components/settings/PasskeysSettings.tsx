import type { Component } from 'solid-js'
import type { PasskeyInfo } from '~/generated/leapmux/v1/user_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createSignal, For, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { Dialog } from '~/components/common/Dialog'
import { Spinner } from '~/components/common/Spinner'
import { PasskeyStepUp } from '~/components/settings/PasskeyStepUp'
import { useAuth } from '~/context/AuthContext'
import { formatErrorMessage } from '~/lib/errors'
import {
  credentialIdFromRegistrationJson,
  isReauthProofRejected,
  loadPasskeys,
  obtainPasskeyReauthProof,
} from '~/lib/passkeyManagement'
import { signalAcceptedPasskeys, signalPasskeyRemoved, startRegistration } from '~/lib/webauthn'
import { errorText, successText, warningText } from '~/styles/shared.css'
import * as styles from './ProfileSettings.css'

type ModalMode = 'add' | 'delete' | 'deactivate' | null

export const PasskeysSettings: Component<{
  onPasskeysChange?: (count: number) => void
}> = (props) => {
  const auth = useAuth()
  const [passkeys, setPasskeys] = createSignal<PasskeyInfo[]>([])
  // The hub's relying party ID for Signal API calls; arrives with the
  // passkey list so this component never derives it locally.
  const [rpId, setRpId] = createSignal('')
  const [loading, setLoading] = createSignal(true)
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)
  const [modalMessage, setModalMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)
  const [modal, setModal] = createSignal<ModalMode>(null)
  const [targetPasskey, setTargetPasskey] = createSignal<PasskeyInfo | null>(null)
  const [currentPassword, setCurrentPassword] = createSignal('')
  const [newPassword, setNewPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [reauthProof, setReauthProof] = createSignal('')
  const [friendlyName, setFriendlyName] = createSignal('')
  const [renamingId, setRenamingId] = createSignal<string | null>(null)
  const [renameValue, setRenameValue] = createSignal('')

  const passwordSet = () => auth.user()?.passwordSet === true
  const passkeyCount = () => passkeys().length

  const notifyCount = (list: PasskeyInfo[]) => {
    props.onPasskeysChange?.(list.length)
  }

  const refresh = async (opts?: { preserveSuccess?: boolean, refreshUser?: boolean }) => {
    const preserveSuccess = opts?.preserveSuccess === true && message()?.type === 'success'
    setLoading(true)
    try {
      const list = await loadPasskeys()
      setRpId(list.rpId)
      setPasskeys(list.passkeys)
      notifyCount(list.passkeys)
      if (opts?.refreshUser === true)
        await auth.refreshUser()
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

  // A stale or invalid reauth proof must not stick around: every retry
  // would resend the same dead proof and fail identically. Clear it so the
  // next attempt asks the user to verify again. The classification rides
  // the connect code, not the message text.
  const dropProofIfInvalid = (e: unknown) => {
    if (isReauthProofRejected(e))
      setReauthProof('')
  }

  const resetModalFields = () => {
    setCurrentPassword('')
    setNewPassword('')
    setConfirmPassword('')
    setReauthProof('')
    setFriendlyName('')
    setTargetPasskey(null)
  }

  const closeModal = () => {
    if (busy())
      return
    setModal(null)
    setModalMessage(null)
    resetModalFields()
  }

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

  const needsReauthForAdd = () => !passwordSet() && passkeyCount() > 0
  const needsReauthForDelete = () => !passwordSet() && passkeyCount() > 1
  const deleteIsDeactivation = () => !passwordSet() && passkeyCount() === 1
  const newPasswordReady = () => newPassword() !== '' && newPassword() === confirmPassword()
  const addConfirmDisabled = () => busy() || (passwordSet() ? !currentPassword() : (needsReauthForAdd() && !reauthProof()))
  const deleteConfirmDisabled = () => {
    if (busy())
      return true
    if (passwordSet())
      return !currentPassword()
    if (deleteIsDeactivation())
      return !reauthProof() || !newPasswordReady()
    return needsReauthForDelete() && !reauthProof()
  }
  const deactivateConfirmDisabled = () => {
    if (busy())
      return true
    if (passwordSet())
      return !currentPassword()
    return !reauthProof() || !newPasswordReady()
  }

  const applyLocalList = (list: PasskeyInfo[]) => {
    setPasskeys(list)
    notifyCount(list)
  }

  const handleReauth = async () => {
    setBusy(true)
    setModalMessage(null)
    try {
      setReauthProof(await obtainPasskeyReauthProof())
    }
    catch (e) {
      const text = formatErrorMessage(e, 'Passkey verification failed')
      if (modal())
        setModalMessage({ type: 'error', text })
      else
        setMessage({ type: 'error', text })
    }
    finally {
      setBusy(false)
    }
  }

  const forceCloseModal = () => {
    setModal(null)
    setModalMessage(null)
    resetModalFields()
  }

  const handleAddPasskey = async () => {
    setBusy(true)
    setModalMessage(null)
    try {
      let proof = reauthProof()
      if (needsReauthForAdd() && !proof)
        proof = await obtainPasskeyReauthProof()
      const begin = await userClient.beginPasskeyRegistration({
        currentPassword: currentPassword(),
        reauthProof: proof,
      })
      const credentialJson = await startRegistration(begin.optionsJson)
      const finish = await userClient.finishPasskeyRegistration({
        sessionId: begin.sessionId,
        credentialJson,
        friendlyName: friendlyName().trim() || 'Passkey',
        currentPassword: currentPassword(),
        reauthProof: proof,
      })
      forceCloseModal()
      setMessage({ type: 'success', text: 'Passkey added.' })
      if (finish.passkey) {
        const added = finish.passkey
        applyLocalList([...passkeys().filter(pk => pk.id !== added.id), added])
      }
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
      dropProofIfInvalid(e)
      setModalMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to add passkey') })
    }
    finally {
      setBusy(false)
    }
  }

  const handleDeletePasskey = async () => {
    const passkey = targetPasskey()
    if (!passkey)
      return
    if (deleteIsDeactivation()) {
      if (!reauthProof() || newPassword() !== confirmPassword() || !newPassword()) {
        setModalMessage({ type: 'error', text: 'Verify with a passkey and set a new password to remove your last passkey.' })
        return
      }
    }
    setBusy(true)
    setModalMessage(null)
    try {
      let proof = reauthProof()
      if ((needsReauthForDelete() || deleteIsDeactivation()) && !proof)
        proof = await obtainPasskeyReauthProof()
      await userClient.deletePasskey({
        id: passkey.id,
        currentPassword: currentPassword(),
        reauthProof: proof,
        newPassword: deleteIsDeactivation() ? newPassword() : '',
      })
      if (rpId())
        signalPasskeyRemoved(rpId(), passkey.credentialId)
      forceCloseModal()
      setMessage({ type: 'success', text: 'Passkey removed.' })
      applyLocalList(passkeys().filter(pk => pk.id !== passkey.id))
      // Only the last-passkey deactivation flips passwordSet; a plain
      // delete changed nothing the local list does not already carry.
      if (deleteIsDeactivation())
        await refresh({ preserveSuccess: true, refreshUser: true })
    }
    catch (e) {
      dropProofIfInvalid(e)
      setModalMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to remove passkey') })
    }
    finally {
      setBusy(false)
    }
  }

  const handleDeactivate = async () => {
    if (!passwordSet()) {
      if (newPassword() !== confirmPassword() || !newPassword()) {
        setModalMessage({ type: 'error', text: 'Verify with a passkey and set a new password to disable passkey sign-in.' })
        return
      }
    }
    setBusy(true)
    setModalMessage(null)
    try {
      let proof = reauthProof()
      if (!passwordSet() && !proof)
        proof = await obtainPasskeyReauthProof()
      await userClient.deactivatePasskeyAuth({
        currentPassword: currentPassword(),
        reauthProof: proof,
        newPassword: passwordSet() ? '' : newPassword(),
      })
      if (rpId()) {
        for (const pk of passkeys())
          signalPasskeyRemoved(rpId(), pk.credentialId)
      }
      forceCloseModal()
      setMessage({ type: 'success', text: 'Passkey sign-in disabled.' })
      applyLocalList([])
      // Deactivation always flips passwordSet on a passkey-only account.
      await refresh({ preserveSuccess: true, refreshUser: true })
    }
    catch (e) {
      dropProofIfInvalid(e)
      setModalMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to deactivate passkeys') })
    }
    finally {
      setBusy(false)
    }
  }

  const startRename = (passkey: PasskeyInfo) => {
    setCurrentPassword('')
    setReauthProof('')
    setRenamingId(passkey.id)
    setRenameValue(passkey.friendlyName || 'Passkey')
  }

  const cancelRename = () => {
    setRenamingId(null)
    setRenameValue('')
    setCurrentPassword('')
    setReauthProof('')
  }

  const saveRename = async (id: string) => {
    setBusy(true)
    setMessage(null)
    try {
      let proof = reauthProof()
      if (!passwordSet() && !proof)
        proof = await obtainPasskeyReauthProof()
      const resp = await userClient.renamePasskey({
        id,
        friendlyName: renameValue().trim() || 'Passkey',
        currentPassword: currentPassword(),
        reauthProof: proof,
      })
      cancelRename()
      setMessage({ type: 'success', text: 'Passkey renamed.' })
      // The response carries the renamed row; a rename changes nothing else.
      if (resp.passkey)
        applyLocalList(passkeys().map(pk => (pk.id === resp.passkey!.id ? resp.passkey! : pk)))
    }
    catch (e) {
      dropProofIfInvalid(e)
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
      <h3 class={styles.sectionHeading}>Passkeys</h3>
      <div class="vstack gap-4">
        <Show when={loading()}>
          <div class={styles.passkeyLoading}><Spinner /></div>
        </Show>
        <Show when={!loading() && passkeys().length === 0}>
          <p class={styles.passkeyEmpty}>No passkeys registered yet.</p>
        </Show>
        <For each={passkeys()}>
          {passkey => (
            <div class={styles.passkeyRow}>
              <div class={styles.passkeyInfo}>
                <Show
                  when={renamingId() !== passkey.id}
                  fallback={(
                    <input
                      type="text"
                      value={renameValue()}
                      onInput={e => setRenameValue(e.currentTarget.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter')
                          void saveRename(passkey.id)
                        if (e.key === 'Escape')
                          cancelRename()
                      }}
                    />
                  )}
                >
                  <span class={styles.passkeyName}>{passkey.friendlyName || 'Passkey'}</span>
                </Show>
                <span class={styles.passkeyMeta}>
                  {passkey.lastUsedAt ? `Last used ${formatPasskeyDate(passkey)}` : `Added ${formatPasskeyDate(passkey)}`}
                </span>
              </div>
              <div class={styles.passkeyActions}>
                <Show when={renamingId() === passkey.id}>
                  <Show when={passwordSet()}>
                    <input
                      type="password"
                      placeholder="Current password"
                      value={currentPassword()}
                      onInput={e => setCurrentPassword(e.currentTarget.value)}
                      autocomplete="current-password"
                    />
                  </Show>
                  <Show when={!passwordSet()}>
                    <PasskeyStepUp proof={reauthProof()} busy={busy()} onVerify={() => void handleReauth()} />
                  </Show>
                  <button
                    type="button"
                    onClick={() => void saveRename(passkey.id)}
                    disabled={busy() || (passwordSet() ? !currentPassword() : !reauthProof())}
                  >
                    Save
                  </button>
                  <button type="button" onClick={cancelRename} disabled={busy()}>Cancel</button>
                </Show>
                <Show when={renamingId() !== passkey.id}>
                  <button type="button" onClick={() => startRename(passkey)} disabled={busy()}>Rename</button>
                  <button type="button" class={styles.passkeyDelete} onClick={() => openDelete(passkey)} disabled={busy()}>
                    Remove
                  </button>
                </Show>
              </div>
            </div>
          )}
        </For>
        <Show when={message()}>
          {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
        </Show>
        <div class={styles.passkeyButtons}>
          <button type="button" onClick={openAdd} disabled={busy() || loading()}>
            Add passkey
          </button>
          <Show when={passkeyCount() > 0}>
            <button type="button" onClick={openDeactivate} disabled={busy() || loading()}>
              Disable passkey sign-in
            </button>
          </Show>
        </div>
      </div>

      <Show when={modal() === 'add'}>
        <Dialog title="Add passkey" onClose={closeModal} busy={busy()}>
          <div class="vstack gap-4">
            <label class={styles.fieldLabel}>
              Name
              <input type="text" value={friendlyName()} onInput={e => setFriendlyName(e.currentTarget.value)} />
            </label>
            <Show when={passwordSet()}>
              <label class={styles.fieldLabel}>
                Current password
                <input
                  type="password"
                  value={currentPassword()}
                  onInput={e => setCurrentPassword(e.currentTarget.value)}
                  autocomplete="current-password"
                />
              </label>
            </Show>
            <Show when={needsReauthForAdd()}>
              <PasskeyStepUp proof={reauthProof()} busy={busy()} onVerify={() => void handleReauth()} />
            </Show>
            <Show when={modalMessage()}>
              {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
            </Show>
            <button type="button" onClick={() => void handleAddPasskey()} disabled={addConfirmDisabled()}>
              {busy() ? 'Adding…' : 'Continue'}
            </button>
          </div>
        </Dialog>
      </Show>

      <Show when={modal() === 'delete'}>
        <Dialog title="Remove passkey" onClose={closeModal} busy={busy()}>
          <div class="vstack gap-4">
            <Show when={deleteIsDeactivation()}>
              <div class={warningText}>
                This is your only sign-in method. Removing it requires setting a password.
              </div>
              <label class={styles.fieldLabel}>
                New password
                <input type="password" value={newPassword()} onInput={e => setNewPassword(e.currentTarget.value)} />
              </label>
              <label class={styles.fieldLabel}>
                Confirm password
                <input type="password" value={confirmPassword()} onInput={e => setConfirmPassword(e.currentTarget.value)} />
              </label>
            </Show>
            <Show when={passwordSet()}>
              <label class={styles.fieldLabel}>
                Current password
                <input
                  type="password"
                  value={currentPassword()}
                  onInput={e => setCurrentPassword(e.currentTarget.value)}
                  autocomplete="current-password"
                />
              </label>
            </Show>
            <Show when={needsReauthForDelete() || deleteIsDeactivation()}>
              <PasskeyStepUp proof={reauthProof()} busy={busy()} onVerify={() => void handleReauth()} />
            </Show>
            <Show when={modalMessage()}>
              {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
            </Show>
            <button type="button" class={styles.passkeyDelete} onClick={() => void handleDeletePasskey()} disabled={deleteConfirmDisabled()}>
              {busy() ? 'Removing…' : 'Remove passkey'}
            </button>
          </div>
        </Dialog>
      </Show>

      <Show when={modal() === 'deactivate'}>
        <Dialog title="Disable passkey sign-in" onClose={closeModal} busy={busy()}>
          <div class="vstack gap-4">
            <p>Removes all passkeys from your account.</p>
            <Show when={passwordSet()}>
              <label class={styles.fieldLabel}>
                Current password
                <input
                  type="password"
                  value={currentPassword()}
                  onInput={e => setCurrentPassword(e.currentTarget.value)}
                  autocomplete="current-password"
                />
              </label>
            </Show>
            <Show when={!passwordSet()}>
              <label class={styles.fieldLabel}>
                New password
                <input type="password" value={newPassword()} onInput={e => setNewPassword(e.currentTarget.value)} />
              </label>
              <label class={styles.fieldLabel}>
                Confirm password
                <input type="password" value={confirmPassword()} onInput={e => setConfirmPassword(e.currentTarget.value)} />
              </label>
              <PasskeyStepUp proof={reauthProof()} busy={busy()} onVerify={() => void handleReauth()} />
            </Show>
            <Show when={modalMessage()}>
              {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
            </Show>
            <button type="button" onClick={() => void handleDeactivate()} disabled={deactivateConfirmDisabled()}>
              {busy() ? 'Disabling…' : 'Disable passkey sign-in'}
            </button>
          </div>
        </Dialog>
      </Show>
    </>
  )
}
