import type { Component } from 'solid-js'
import type { ShellKind } from './workerRegistrationCommands'
import type { PillOptions } from '~/components/common/pillOptions'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import Mail from 'lucide-solid/icons/mail'
import { createMemo, createSignal, onCleanup, onMount, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { PillGroup } from '~/components/common/PillGroup'
import { Tooltip } from '~/components/common/Tooltip'
import { useAuth } from '~/context/AuthContext'
import { useCopyButton } from '~/hooks/useCopyButton'
import { getWorkerHubUrl, isEmailEnabled, isSoloMode } from '~/lib/systemInfo'
import { errorText } from '~/styles/shared.css'
import * as styles from './RegisterWorkerDialog.css'
import { workerRegistrationCommands } from './workerRegistrationCommands'

const shellOptions: PillOptions<ShellKind> = [
  { key: 'posix', label: 'POSIX' },
  { key: 'powershell', label: 'PowerShell' },
]

interface RegisterWorkerDialogProps {
  onClose: () => void
}

// Cadence (frontend, ms): how often this dialog checks whether the key is
// close enough to expiry to be worth extending. The backend rejects
// extensions while remaining > 2 minutes, so a 30-second tick gives plenty of
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

  const [shell, setShell] = createSignal<ShellKind>('posix')

  const commands = createMemo(() => {
    const k = registrationKey()
    if (!k)
      return null
    // Prefer the URL the hub advertises (unix:/npipe: in desktop's
    // local-only mode); fall back to the browser origin everywhere else
    // since that already reflects any reverse-proxy hostname.
    const hubUrl = getWorkerHubUrl() || window.location.origin
    return workerRegistrationCommands({
      hubUrl,
      registrationKey: k,
      shell: shell(),
    })
  })

  // A key or endpoint with characters that cannot form a safe displayed
  // command refuses every clipboard write; the user sees the refusal.
  const { copied: copiedRegister, copy: copyRegister } = useCopyButton(() => commands()?.register ?? '')
  const { copied: copiedStart, copy: copyStart } = useCopyButton(() => commands()?.start ?? '')

  const canEmail = createMemo(() => Boolean(auth.user()?.emailVerified))

  // Revoke the key on close. This discards the errors because the
  // dialog closes regardless — the key expires on its own.
  onCleanup(() => {
    const k = registrationKey()
    if (k)
      workerClient.revokeRegistrationKey({ registrationKey: k }).catch(() => {})
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
  // so the threshold check on the client only avoids wasted RPCs. The
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
        if (!resp.registrationKey) {
          setError('The Hub did not return a registration key. Close this dialog and try again.')
          return
        }
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
    const command = commands()?.register
    if (!k || !command || !canEmail() || emailing() || emailSent())
      return
    setEmailing(true)
    setError(null)
    try {
      await workerClient.emailRegistrationInstructions({
        registrationKey: k,
        command,
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
    <Dialog title="Register worker" class={styles.dialog} onClose={props.onClose}>
      <section class={styles.body}>
        <p>
          Run the command below on the machine where the worker should run.
        </p>
        <p>
          This registration key is only valid while this dialog stays open.
          If you close the dialog, the key is destroyed and you'll need to
          start over.
        </p>

        <Show when={registrationKey()} fallback={<Show when={!error()}><p>Generating registration key…</p></Show>}>
          <div class={styles.shellRow} role="radiogroup" aria-label="Shell">
            <PillGroup
              label="Shell"
              selectedKey={shell()}
              onSelect={setShell}
              options={shellOptions}
            />
          </div>
          <Show when={commands()} fallback={<p class={errorText}>The Hub address or registration key cannot form a safe command. Check the Hub configuration or generate a new key.</p>}>
            <p>1. Register the worker.</p>
            <pre class={styles.command} data-testid="registration-command">{commands()!.register}</pre>
            <p>2. After registration succeeds, start the worker.</p>
            <pre class={styles.command} data-testid="start-command">{commands()!.start}</pre>
            <p>Keep this dialog open until registration completes.</p>
          </Show>
        </Show>

        <Show when={error()}>
          {msg => <p class={errorText}>{msg()}</p>}
        </Show>
      </section>
      <footer class={actionsFooter}>
        <button type="button" class="outline" onClick={() => props.onClose()}>Cancel</button>

        <Show when={registrationKey()}>
          {/* Hide the action when there's no way to send mail: solo mode
              ships without SMTP and the bootstrap user has no email; on
              hub mode, an unconfigured smtp_host means the corresponding
              RPC would return FailedPrecondition. Either way, a
              permanently-disabled button would mislead users — drop it
              entirely. */}
          <Show when={!isSoloMode() && isEmailEnabled()}>
            <Tooltip text={!commands()
              ? 'A safe registration command is required before sending email'
              : canEmail()
                ? 'Email this command to your verified address'
                : 'Verify your email to enable this'}
            >
              <button
                type="button"
                data-testid="email-registration-instructions"
                disabled={!commands() || !canEmail() || emailing() || emailSent()}
                onClick={() => void handleSendEmail()}
              >
                <Icon icon={Mail} size="sm" />
                {' '}
                <Show
                  when={emailSent()}
                  fallback={emailing()
                    ? 'Sending…'
                    : 'Send email'}
                >
                  <span>{`Sent to ${auth.user()?.email ?? ''}`}</span>
                </Show>
              </button>
            </Tooltip>
          </Show>

          <Tooltip text="Copy the registration command" ariaLabel>
            <button
              type="button"
              data-testid="copy-registration-command"
              disabled={!commands()}
              onClick={() => void copyRegister()}
            >
              <Icon
                icon={copiedRegister()
                  ? Check
                  : Copy}
                size="sm"
              />
              {' '}
              {copiedRegister()
                ? 'Copied'
                : 'Copy registration command'}
            </button>
          </Tooltip>
          <Tooltip text="Copy the start command" ariaLabel>
            <button
              type="button"
              data-testid="copy-start-command"
              disabled={!commands()}
              onClick={() => void copyStart()}
            >
              <Icon
                icon={copiedStart()
                  ? Check
                  : Copy}
                size="sm"
              />
              {' '}
              {copiedStart()
                ? 'Copied'
                : 'Copy start command'}
            </button>
          </Tooltip>
        </Show>
      </footer>
    </Dialog>
  )
}
