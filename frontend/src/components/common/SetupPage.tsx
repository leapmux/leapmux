import type { Component } from 'solid-js'

import { useNavigate } from '@solidjs/router'
import { createSignal, onMount, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { isSetupRequired, loadSystemInfo } from '~/lib/systemInfo'
import { cardNarrow } from '~/styles/shared.css'
import * as styles from './LoginPage.css'
import { SignupForm } from './SignupForm'

export const SetupPage: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()
  const [ready, setReady] = createSignal(false)

  onMount(() => {
    if (!isSetupRequired()) {
      navigate('/login', { replace: true })
      return
    }
    setReady(true)
  })

  return (
    <Show when={ready()} fallback={null}>
      <div class={styles.container}>
        <div class={`card ${cardNarrow}`}>
          <h1>Welcome to LeapMux</h1>
          <SignupForm
            submitLabel="Create account"
            submittingLabel="Creating account..."
            errorPrefix="Setup failed"
            allowAdminUsername
            header={<p>Create the first administrator account to get started.</p>}
            onSuccess={(resp, slug) => {
              loadSystemInfo(true)
              auth.setAuth(resp.user!)
              navigate(`/o/${slug}`, { replace: true })
            }}
          />
        </div>
      </div>
    </Show>
  )
}
