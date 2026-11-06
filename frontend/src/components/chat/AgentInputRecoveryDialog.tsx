import type { Component } from 'solid-js'
import type { InputRecoveryActions, InputRecoveryDiscovery, InputRecoveryResult, InputRecoveryTarget } from '~/models/agentInputRecovery'
import { createMemo, createSignal, For, onCleanup, onMount, Show, untrack } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Dialog } from '~/components/common/Dialog'
import { formatErrorMessage } from '~/lib/errors'
import * as styles from './AgentInputRecoveryDialog.css'

interface AgentInputRecoveryDialogProps {
  current: InputRecoveryTarget
  actions: Pick<InputRecoveryActions, 'discoverTargets' | 'recoverTargets'>
  onClose: () => void
}

export const AgentInputRecoveryDialog: Component<AgentInputRecoveryDialogProps> = (props) => {
  // A prop update cannot transfer an open confirmation to another process.
  const current = untrack(() => props.current)
  const actions = untrack(() => props.actions)
  const providerLabel = agentProviderLabel(current.recovery.agentProvider)
  const [discovery, setDiscovery] = createSignal<InputRecoveryDiscovery>()
  const [loading, setLoading] = createSignal(true)
  const [loadError, setLoadError] = createSignal('')
  const [all, setAll] = createSignal(false)
  const [busy, setBusy] = createSignal(false)
  const [results, setResults] = createSignal<readonly InputRecoveryResult[]>()
  let alive = true
  onCleanup(() => {
    alive = false
  })

  const checkTargets = async () => {
    setLoading(true)
    setLoadError('')
    setDiscovery(undefined)
    try {
      const found = await actions.discoverTargets(current)
      if (alive)
        setDiscovery(found)
    }
    catch (error) {
      if (alive)
        setLoadError(formatErrorMessage(error, 'The affected tabs could not be checked.'))
    }
    finally {
      if (alive)
        setLoading(false)
    }
  }
  onMount(() => {
    void checkTargets()
  })

  const selected = createMemo(() => {
    const found = discovery()
    if (!found)
      return []
    return all()
      ? found.targets
      : [found.current]
  })
  const workerCount = createMemo(() => new Set(selected().map(target => target.workerId)).size)
  const canRecover = () => !loading() && !busy() && !loadError() && selected().length > 0
    && (!all() || discovery()?.failures.length === 0)

  const recover = async () => {
    if (!canRecover())
      return
    const approved = [
      ...selected(),
    ]
    setBusy(true)
    try {
      const completed = await actions.recoverTargets(approved)
      if (completed.length !== approved.length)
        throw new Error('Recovery could not be confirmed for every selected tab. Check these tabs before trying again.')
      if (alive)
        setResults(completed)
    }
    catch (error) {
      const message = formatErrorMessage(error, 'Recovery could not be confirmed. Check these tabs before trying again.')
      if (alive)
        setResults(approved.map((target): InputRecoveryResult => ({ target, status: 'unconfirmed', message })))
    }
    finally {
      if (alive)
        setBusy(false)
    }
  }

  const renderTarget = (target: InputRecoveryTarget) => (
    <div class={styles.target}>
      <strong>{target.title || `${providerLabel} tab`}</strong>
      <span>{target.workerName || target.workerId}</span>
    </div>
  )

  return (
    <Dialog title="Recover login" busy={busy()} onClose={() => props.onClose()} data-testid="input-recovery-dialog">
      <section>
        <Show
          when={results()}
          fallback={(
            <>
              <p>
                Recovery uses each worker's current
                {' '}
                {providerLabel}
                {' '}
                login. Any running work in the selected tabs and their subagents will be interrupted.
              </p>
              <p>Conversations are kept. Input that has not reached an agent can continue from the queue after recovery. Failed and uncertain inputs are not retried. Manual pauses remain in effect.</p>
              <Show when={loading()}>
                <p role="status">Checking affected tabs…</p>
              </Show>
              <Show when={loadError()}>
                <p role="alert">{loadError()}</p>
                <button type="button" class="outline" onClick={() => { void checkTargets() }}>Retry checks</button>
              </Show>
              <Show when={!loading() && discovery()}>
                {found => (
                  <>
                    <label class={styles.batchChoice}>
                      <input type="checkbox" checked={all()} disabled={busy()} onChange={event => setAll(event.currentTarget.checked)} />
                      Recover all
                      {' '}
                      {providerLabel}
                      {' '}
                      tabs with login issues
                    </label>
                    <p role="status" data-testid="input-recovery-summary">
                      {selected().length}
                      {' '}
                      {providerLabel}
                      {' '}
                      {selected().length === 1
                        ? 'tab'
                        : 'tabs'}
                      {' '}
                      on
                      {' '}
                      {workerCount()}
                      {' '}
                      {workerCount() === 1
                        ? 'worker'
                        : 'workers'}
                    </p>
                    <p>Only the tabs in this confirmation are included. Healthy tabs and later login issues are excluded.</p>
                    <details class={styles.targetDetails}>
                      <summary>View affected tabs</summary>
                      <ul class={styles.targetList} data-testid="input-recovery-targets">
                        <For each={selected()}>{target => <li>{renderTarget(target)}</li>}</For>
                      </ul>
                    </details>
                    <Show when={found().failures.length > 0}>
                      <div role="alert">
                        <p>Some workers could not be checked. Recover this tab only, or retry the checks.</p>
                        <ul class={styles.targetList}>
                          <For each={found().failures}>
                            {failure => (
                              <li>
                                {failure.workerName || failure.workerId}
                                :
                                {' '}
                                {failure.message}
                              </li>
                            )}
                          </For>
                        </ul>
                        <button type="button" class="outline" disabled={busy()} onClick={() => { void checkTargets() }}>Retry checks</button>
                      </div>
                    </Show>
                  </>
                )}
              </Show>
              <Show when={busy()}><p role="status">Recovering the selected tabs…</p></Show>
            </>
          )}
        >
          {completed => (
            <>
              <p role="status" data-testid="input-recovery-result-summary">
                Recovered
                {' '}
                {completed().filter(result => result.status === 'recovered').length}
                {' '}
                of
                {' '}
                {completed().length}
                {' '}
                {providerLabel}
                {' '}
                {completed().length === 1
                  ? 'tab'
                  : 'tabs'}
                .
              </p>
              <ul class={styles.targetList} data-testid="input-recovery-results">
                <For each={completed()}>
                  {result => (
                    <li>
                      {renderTarget(result.target)}
                      <p>
                        {result.status === 'recovered'
                          ? 'Recovered'
                          : result.message}
                      </p>
                    </li>
                  )}
                </For>
              </ul>
            </>
          )}
        </Show>
      </section>
      <footer class={actionsFooter}>
        <Show when={!results()}>
          <button type="button" class="outline" disabled={busy()} onClick={() => props.onClose()}>Cancel</button>
          <button type="button" disabled={!canRecover()} onClick={() => { void recover() }}>Recover login</button>
        </Show>
        <Show when={results()}>
          <button type="button" onClick={() => props.onClose()}>Done</button>
        </Show>
      </footer>
    </Dialog>
  )
}
