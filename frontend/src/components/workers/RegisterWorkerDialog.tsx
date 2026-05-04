import type { Component } from 'solid-js'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import Mail from 'lucide-solid/icons/mail'
import { createMemo, createSignal, onCleanup, onMount, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { useAuth } from '~/context/AuthContext'
import { useCopyButton } from '~/hooks/useCopyButton'
import { getWorkerHubUrl, isSoloMode } from '~/lib/systemInfo'
import { errorText } from '~/styles/shared.css'
import * as styles from './RegisterWorkerDialog.css'

interface RegisterWorkerDialogProps {
  onClose: () => void
}

// Cadence (frontend, ms): how often we check whether the key is close
// enough to expiry to be worth extending. The backend rejects extensions
// while remaining > 2 minutes, so a 30-second tick gives us plenty of
// in-window attempts before the 5-minute TTL elapses.
const EXTEND_TICK_MS = 30_000
const EXTEND_THRESHOLD_MS = 2 * 60 * 1000

export const RegisterWorkerDialog: Component<RegisterWorkerDialogProps> = (props) => {
  const auth = useAuth()
  const [registrationKey, setRegistrationKey] = createSignal<string | null>(null)
  const [expiresAt, setExpiresAt] = createSignal<Date | null>(null)
  const [error, setError] = createSignal<string | null>(null)
  const [emailing, setEmailing] = createSignal(false)
  const [emailSent, setEmailSent] = createSignal(false)

  const command = createMemo(() => {
    const k = registrationKey()
    if (!k)
      return ''
    // Prefer the URL the hub advertises (unix:/npipe: in desktop's
    // local-only mode); fall back to the browser origin everywhere else
    // since that already reflects any reverse-proxy hostname.
    const hubUrl = getWorkerHubUrl() || window.location.origin
    return `leapmux worker --hub ${hubUrl} --registration-key ${k}`
  })

  const { copied, copy } = useCopyButton(() => command())

  const canEmail = createMemo(() => Boolean(auth.user()?.emailVerified))

  // Soft-delete the key on close. Errors are swallowed because the
  // dialog is going away regardless — the key will expire on its own.
  onCleanup(() => {
    const k = registrationKey()
    if (k)
      workerClient.deleteRegistrationKey({ registrationKey: k }).catch(() => {})
  })

  // Single onMount with a synchronous timer setup followed by a
  // fire-and-forget IIFE for the key-mint RPC. The shape matters:
  //   - onCleanup(clearInterval) MUST register before any await so it
  //     binds to the component's reactive owner. An `onMount(async …)`
  //     that awaited before onCleanup would lose the binding on
  //     unmount-during-pending and leak the interval.
  //   - The IIFE is fire-and-forget because no other lifecycle work
  //     depends on the create finishing; the timer body checks
  //     registrationKey()/expiresAt() each tick and no-ops until the
  //     signals are populated.
  // Two separate onMount blocks would also work, but combining keeps
  // the timer + the work it depends on (the create that populates the
  // signals it reads) in a single visual unit.
  //
  // Auto-extension loop: only fires inside the last EXTEND_THRESHOLD_MS
  // before expiry. The backend's anti-spam guard refuses earlier calls,
  // so the threshold check on the client just avoids burning RPCs. The
  // threshold is purposely smaller than the backend's anti-spam buffer
  // — staying inside a single tick avoids alignment edge cases.
  onMount(() => {
    const timer = setInterval(async () => {
      const exp = expiresAt()
      const k = registrationKey()
      if (!exp || !k)
        return
      const remaining = exp.getTime() - Date.now()
      if (remaining > 0 && remaining < EXTEND_THRESHOLD_MS) {
        try {
          const resp = await workerClient.extendRegistrationKey({ registrationKey: k })
          if (resp.expiresAt)
            setExpiresAt(timestampDate(resp.expiresAt))
        }
        catch {
          // The next tick will retry. If extension is permanently broken
          // (key was already consumed elsewhere), the user can reopen.
        }
      }
    }, EXTEND_TICK_MS)
    onCleanup(() => clearInterval(timer))

    void (async () => {
      try {
        const resp = await workerClient.createRegistrationKey({})
        setRegistrationKey(resp.registrationKey)
        if (resp.expiresAt)
          setExpiresAt(timestampDate(resp.expiresAt))
      }
      catch (e) {
        setError(`Failed to create registration key: ${e}`)
      }
    })()
  })

  async function handleSendEmail() {
    const k = registrationKey()
    if (!k)
      return
    setEmailing(true)
    setError(null)
    try {
      await workerClient.emailRegistrationInstructions({
        registrationKey: k,
        command: command(),
      })
      setEmailSent(true)
    }
    catch (e) {
      setError(`Failed to send email: ${e}`)
    }
    finally {
      setEmailing(false)
    }
  }

  return (
    <Dialog title="Register a new worker" class={styles.dialog} onClose={props.onClose}>
      <section class={styles.body}>
        <p>
          Run the command below on the machine where the worker should run.
        </p>
        <p>
          This registration key is only valid while this dialog stays open.
          If you close the dialog, the key is destroyed and you'll need to
          start over.
        </p>

        <Show when={registrationKey()} fallback={<p>Generating registration key…</p>}>
          <pre class={styles.command} data-testid="registration-command">{command()}</pre>
        </Show>

        <Show when={error()}>
          {msg => <p class={errorText}>{msg()}</p>}
        </Show>
      </section>
      <footer>
        <button type="button" class="outline" onClick={() => props.onClose()}>Cancel</button>

        <Show when={registrationKey()}>
          {/* Solo mode has no SMTP and the bootstrap user has no email — hide the action instead of showing a permanently-disabled button. */}
          <Show when={!isSoloMode()}>
            <button
              type="button"
              data-testid="email-registration-instructions"
              disabled={!canEmail() || emailing() || emailSent()}
              title={canEmail() ? 'Email this command to your verified address' : 'Verify your email to enable this'}
              onClick={() => void handleSendEmail()}
            >
              <Icon icon={Mail} size="sm" />
              {' '}
              <Show when={emailSent()} fallback={emailing() ? 'Sending…' : 'Send email'}>
                <span>{`Sent to ${auth.user()?.email ?? ''}`}</span>
              </Show>
            </button>
          </Show>

          <button
            type="button"
            data-testid="copy-registration-command"
            onClick={() => void copy()}
          >
            <Icon icon={copied() ? Check : Copy} size="sm" />
            {' '}
            {copied() ? 'Copied' : 'Copy command'}
          </button>
        </Show>
      </footer>
    </Dialog>
  )
}
