import type { Component } from 'solid-js'
import type { CliInstallResult, CliPathStatus, CliPathTargetKind } from '~/api/platformBridge'
import { createSignal, Match, Show, Switch } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { Dialog } from '~/components/common/Dialog'
import { useCopyButton } from '~/hooks/useCopyButton'

// When the install action will clobber a real (non-symlink) file at the
// destination, render the primary action as a two-click danger ConfirmButton
// matching the rest of the app's destructive-confirmation pattern (see
// AppShellDialogs.tsx). Symlinks — even outdated ones — aren't user
// content, so they get the regular single-click primary. 'unknown' (e.g. a
// permission error reading the target) defaults to the safer two-click flow.
function clobbersRealFile(targetKind: CliPathTargetKind): boolean {
  return targetKind === 'regular_file' || targetKind === 'unknown'
}

interface CliPathDialogProps {
  // The status reported by the sidecar. Callers should only mount the dialog
  // for `missing` / `mismatch`; `ok` and `unavailable` short-circuit before
  // reaching the UI.
  status: CliPathStatus & { state: 'missing' | 'mismatch' }
  onClose: () => void
}

// After invoking cliInstallSymlink, the dialog body switches to one of these
// terminal sub-states until the user dismisses it. ALREADY_EXISTS_REAL_FILE
// is intentionally not represented: the sidecar's status pre-check feeds
// `targetKind` into the dialog so we send force=true upfront for the
// regular-file case (via the ConfirmButton two-click arming) and never
// land in that response branch.
type InstallOutcome
  = | { kind: 'needs_sudo', command: string }
    | { kind: 'parent_missing', path: string, command: string }
    | { kind: 'io_error', message: string }

export const CliPathDialog: Component<CliPathDialogProps> = (props) => {
  const [busy, setBusy] = createSignal(false)
  const [outcome, setOutcome] = createSignal<InstallOutcome | null>(null)

  // Single copy-button instance for whichever sudo command the outcome
  // happens to surface. The hook handles the 2-second "Copied" flicker.
  const { copied, copy } = useCopyButton(() => {
    const o = outcome()
    if (!o)
      return undefined
    if (o.kind === 'needs_sudo' || o.kind === 'parent_missing')
      return o.command
    return undefined
  })

  const handleResult = (result: CliInstallResult) => {
    switch (result.result) {
      case 'ok':
        // Symlink installed successfully — close the dialog. The sessionStorage
        // gate in AppShell prevents the check from re-firing in the same session,
        // so the user won't see this dialog again until app restart.
        props.onClose()
        return
      case 'needs_sudo':
        setOutcome({ kind: 'needs_sudo', command: result.command })
        return
      case 'parent_missing':
        setOutcome({ kind: 'parent_missing', path: result.path, command: result.command })
        return
      case 'already_exists_real_file':
        // Should be unreachable: the pre-checked targetKind upgrades the
        // primary action to the danger ConfirmButton and the second click
        // sends force=true. If we still get here (filesystem race between
        // status and install), fall through to a generic error.
        setOutcome({ kind: 'io_error', message: `Unexpected file at ${result.path}` })
        return
      case 'io_error':
        setOutcome({ kind: 'io_error', message: result.message })
    }
  }

  const install = async (force = false) => {
    setBusy(true)
    try {
      const result = await platformBridge.cliInstallSymlink(force)
      handleResult(result)
    }
    catch (err) {
      setOutcome({ kind: 'io_error', message: err instanceof Error ? err.message : String(err) })
    }
    finally {
      setBusy(false)
    }
  }

  // The dialog renders one of three layouts:
  //   1. mismatch warning — no install offered
  //   2. missing prompt — primary "Install" CTA, secondary "Not now"
  //   3. install outcome — sudo command + Copy, or error explanation
  return (
    <Show
      when={outcome()}
      fallback={(
        <Switch>
          <Match when={props.status.state === 'mismatch' && props.status}>
            {(s) => {
              const status = s() as Extract<CliPathStatus, { state: 'mismatch' }>
              const dangerous = clobbersRealFile(status.targetKind)
              return (
                <Dialog title="A different leapmux is on your PATH" onClose={props.onClose} busy={busy()}>
                  <section>
                    <p>
                      The
                      {' '}
                      <code>leapmux</code>
                      {' '}
                      command on your PATH resolves to a different binary
                      than the one shipped with this app. Using the existing
                      one may produce unexpected results.
                    </p>
                    <p>
                      Resolved to:
                      {' '}
                      <code>{status.resolved}</code>
                    </p>
                  </section>
                  <footer>
                    <button type="button" class="outline" disabled={busy()} onClick={props.onClose}>
                      Dismiss
                    </button>
                    {/* Mismatch always passes force=true: the user has read
                        the warning and is opting to replace whatever sits at
                        /usr/local/bin/leapmux. When that entry is a real
                        file we render the two-click danger ConfirmButton;
                        for symlinks (the common case) a single click is
                        enough since symlinks aren't user content. */}
                    <Show
                      when={dangerous}
                      fallback={(
                        <button type="button" disabled={busy()} onClick={() => install(true)}>
                          Replace
                        </button>
                      )}
                    >
                      <ConfirmButton
                        data-variant="danger"
                        disabled={busy()}
                        onClick={() => install(true)}
                      >
                        Replace
                      </ConfirmButton>
                    </Show>
                  </footer>
                </Dialog>
              )
            }}
          </Match>
          <Match when={props.status.state === 'missing' && props.status}>
            {(s) => {
              const status = s() as Extract<CliPathStatus, { state: 'missing' }>
              const dangerous = clobbersRealFile(status.targetKind)
              return (
                <Dialog title="Install leapmux command-line tool?" onClose={props.onClose} busy={busy()}>
                  <section>
                    <p>
                      Add the
                      {' '}
                      <code>leapmux</code>
                      {' '}
                      command to your PATH by creating a symlink at
                      {' '}
                      <code>{status.target}</code>
                      .
                    </p>
                    <Show when={dangerous}>
                      <p>
                        A file already exists at
                        {' '}
                        <code>{status.target}</code>
                        . Installing will replace it.
                      </p>
                    </Show>
                  </section>
                  <footer>
                    <button type="button" class="outline" disabled={busy()} onClick={props.onClose}>
                      Not now
                    </button>
                    <Show
                      when={dangerous}
                      fallback={(
                        <button type="button" disabled={busy()} onClick={() => install(false)}>
                          Install
                        </button>
                      )}
                    >
                      <ConfirmButton
                        data-variant="danger"
                        disabled={busy()}
                        onClick={() => install(true)}
                      >
                        Replace and Install
                      </ConfirmButton>
                    </Show>
                  </footer>
                </Dialog>
              )
            }}
          </Match>
        </Switch>
      )}
    >
      {(o) => {
        const outcomeValue = o()
        return (
          <Dialog title="Install leapmux command-line tool" onClose={props.onClose}>
            <section>
              <Switch>
                <Match when={outcomeValue.kind === 'needs_sudo' && outcomeValue}>
                  {(v) => {
                    const out = v() as Extract<InstallOutcome, { kind: 'needs_sudo' }>
                    return (
                      <>
                        <p>
                          Creating the symlink requires elevated permissions. Run this in a
                          terminal:
                        </p>
                        <pre><code>{out.command}</code></pre>
                      </>
                    )
                  }}
                </Match>
                <Match when={outcomeValue.kind === 'parent_missing' && outcomeValue}>
                  {(v) => {
                    const out = v() as Extract<InstallOutcome, { kind: 'parent_missing' }>
                    return (
                      <>
                        <p>
                          The directory
                          {' '}
                          <code>{out.path}</code>
                          {' '}
                          does not exist on this machine.
                          Run this in a terminal to create it and install the CLI:
                        </p>
                        <pre><code>{out.command}</code></pre>
                      </>
                    )
                  }}
                </Match>
                <Match when={outcomeValue.kind === 'io_error' && outcomeValue}>
                  {(v) => {
                    const out = v() as Extract<InstallOutcome, { kind: 'io_error' }>
                    return (
                      <p>
                        Could not install the CLI:
                        {' '}
                        <code>{out.message}</code>
                      </p>
                    )
                  }}
                </Match>
              </Switch>
            </section>
            <footer>
              <Show when={outcomeValue.kind === 'needs_sudo' || outcomeValue.kind === 'parent_missing'}>
                <button type="button" class="outline" onClick={copy}>
                  {copied() ? 'Copied' : 'Copy command'}
                </button>
              </Show>
              <button type="button" onClick={props.onClose}>
                Close
              </button>
            </footer>
          </Dialog>
        )
      }}
    </Show>
  )
}
