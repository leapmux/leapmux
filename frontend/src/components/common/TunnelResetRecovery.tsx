import type { Component } from 'solid-js'
import { createSignal, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'

/** Recovery for an outgoing desktop tunnel epoch whose native reset failed. */
export const TunnelResetRecovery: Component = () => {
  const auth = useAuth()
  const [retrying, setRetrying] = createSignal(false)

  const retry = async () => {
    if (retrying())
      return
    setRetrying(true)
    try {
      await auth.retryTunnelReset?.()
    }
    catch {
      // AuthContext retains the actionable error for this surface.
    }
    finally {
      setRetrying(false)
    }
  }

  return (
    <Show when={auth.tunnelResetError?.()}>
      {message => (
        <div role="alert" data-testid="tunnel-reset-recovery">
          <p>{message()}</p>
          <button type="button" disabled={retrying()} onClick={() => { void retry() }}>
            {retrying()
              ? 'Retrying...'
              : 'Retry desktop tunnel cleanup'}
          </button>
        </div>
      )}
    </Show>
  )
}
