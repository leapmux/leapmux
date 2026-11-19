import type { Component } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { createSignal, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { formatErrorMessage } from '~/lib/errors'
import { openHubPage } from '~/lib/hubSite'
import { loadSystemInfo } from '~/lib/systemInfo'
import * as styles from './ContinueInBrowser.css'

/**
 * Shown in desktop remote-Hub mode in place of a ceremony page that needs
 * the Hub's origin (sign-up, setup, recovery, email verification, step-up).
 * Opens the corresponding Hub page in the system browser.
 */
export const ContinueInBrowser: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()
  const [error, setError] = createSignal('')
  const [checking, setChecking] = createSignal(false)

  const currentPath = (): string => {
    return `${window.location.pathname}${window.location.search}`
  }

  const handleOpen = async (): Promise<void> => {
    try {
      await openHubPage(currentPath())
    }
    catch (err) {
      setError(formatErrorMessage(err))
    }
  }

  const handleCheckAgain = async (): Promise<void> => {
    setChecking(true)
    setError('')
    try {
      await loadSystemInfo(true)
      await auth.refreshUser()
    }
    catch (err) {
      setError(formatErrorMessage(err))
    }
    finally {
      setChecking(false)
    }
  }

  return (
    <div class={styles.container}>
      <h1 class={styles.title}>Continue in your browser</h1>
      <p class={styles.description}>
        This step runs on the Hub's own page.
      </p>
      <div class={styles.actions}>
        <button type="button" onClick={() => void handleOpen()}>
          Open in browser
        </button>
        <button type="button" onClick={() => void handleCheckAgain()} disabled={checking()}>
          Check again
        </button>
      </div>
      <Show when={error()}>
        <p class={styles.error}>{error()}</p>
      </Show>
      <button type="button" class={styles.backLink} onClick={() => navigate('/login')}>
        Back to sign-in
      </button>
    </div>
  )
}
