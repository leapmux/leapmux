import type { Component } from 'solid-js'
import { createSignal, onCleanup, onMount, Show } from 'solid-js'
import { animateWindowResize, waitForWailsBindings } from '~/api/desktopBridge'
import * as styles from './LauncherView.css'

const httpSchemeRegex = /^https?:\/\//i

/**
 * LauncherView is the mode selection UI shown when the desktop app starts.
 * It replaces the old vanilla HTML launcher and is rendered within the
 * SolidJS SPA when running in desktop mode and not yet connected.
 */
export const LauncherView: Component<{ onConnected: () => void }> = (props) => {
  const [mode, setMode] = createSignal<'solo' | 'distributed'>('solo')
  const [hubUrl, setHubUrl] = createSignal('')
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal('')
  const [version, setVersion] = createSignal('')
  const [fdaGranted, setFdaGranted] = createSignal(true)
  const [visible, setVisible] = createSignal(false)
  let fdaPollTimer: ReturnType<typeof setInterval> | null = null
  let containerRef: HTMLDivElement | undefined

  const app = () => window.go!.main.App

  const isValidHubUrl = (value: string): boolean => {
    let s = value.trim()
    if (!s)
      return false
    if (!httpSchemeRegex.test(s))
      s = `https://${s}`
    try {
      const u = new URL(s)
      return u.protocol === 'http:' || u.protocol === 'https:'
    }
    catch {
      return false
    }
  }

  const canConnect = () => {
    if (mode() === 'distributed')
      return isValidHubUrl(hubUrl())
    return fdaGranted()
  }

  const stopFDAPoll = () => {
    if (fdaPollTimer !== null) {
      clearInterval(fdaPollTimer)
      fdaPollTimer = null
    }
  }

  const checkFDA = async () => {
    try {
      const granted = await app().CheckFullDiskAccess()
      const wasBlocked = !fdaGranted()
      setFdaGranted(granted)
      if (granted && wasBlocked) {
        stopFDAPoll()
        app().Restart()
      }
    }
    catch {
      setFdaGranted(true)
    }
  }

  const startFDAPoll = () => {
    stopFDAPoll()
    fdaPollTimer = setInterval(checkFDA, 1000)
  }

  const selectMode = async (m: 'solo' | 'distributed') => {
    setMode(m)
    setError('')
    if (m === 'solo') {
      await checkFDA()
      if (!fdaGranted())
        startFDAPoll()
    }
    else {
      stopFDAPoll()
    }
  }

  const fadeOut = (): Promise<void> => {
    if (!containerRef)
      return Promise.resolve()
    setVisible(false)
    return new Promise((resolve) => {
      containerRef!.addEventListener('transitionend', () => resolve(), { once: true })
      // Fallback in case transitionend doesn't fire.
      setTimeout(resolve, 400)
    })
  }

  const connect = async () => {
    setLoading(true)
    setError('')
    try {
      if (mode() === 'solo') {
        await app().ConnectSolo()
      }
      else {
        await app().ConnectDistributed(hubUrl().trim())
      }
      // Fade out UI, then animate window to saved or default dimensions.
      await fadeOut()
      const config = await app().GetConfig()
      const targetW = config.window_width > 0 ? config.window_width : 1280
      const targetH = config.window_height > 0 ? config.window_height : 800
      await animateWindowResize(targetW, targetH)
      props.onConnected()
    }
    catch (err) {
      setVisible(true)
      setError(err instanceof Error ? err.message : String(err))
      setLoading(false)
    }
  }

  onMount(async () => {
    // Wait for Wails to inject window.go bindings (async on reload).
    await waitForWailsBindings()

    try {
      const ver = await app().GetVersion()
      if (ver)
        setVersion(ver)
    }
    catch { /* ignore */ }

    try {
      const config = await app().GetConfig()
      if (config.mode === 'distributed' && config.hub_url) {
        setHubUrl(config.hub_url)
      }
      if (config.mode) {
        setMode(config.mode as 'solo' | 'distributed')
      }

      // Animate resize to launcher dimensions while still invisible
      // (opacity 0), so the user sees a smooth resize without content.
      await animateWindowResize(900, 680)

      // Auto-connect if user has previously connected.
      if (config.mode) {
        if (config.mode === 'solo') {
          await checkFDA()
          if (!fdaGranted()) {
            startFDAPoll()
            setVisible(true)
            return
          }
        }
        // Auto-connect silently — don't fade in unless it takes > 1s.
        const showTimer = setTimeout(setVisible, 1000, true)
        await connect()
        clearTimeout(showTimer)
      }
      else {
        // First launch or returning from Switch Mode — show launcher.
        await checkFDA()
        if (!fdaGranted())
          startFDAPoll()
        setVisible(true)
      }
    }
    catch { /* ignore */ }
  })

  onCleanup(() => stopFDAPoll())

  return (
    <div ref={containerRef} class={styles.container} style={{ opacity: visible() ? 1 : 0 }}>
      <div class={styles.header}>
        <h1 class={styles.title}>LeapMux</h1>
        <p class={styles.subtitle}>Choose how you'd like to connect</p>
      </div>

      <div class={styles.modeCards}>
        <button
          class={`${styles.modeCard} ${mode() === 'solo' ? styles.modeCardSelected : ''}`}
          onClick={() => selectMode('solo')}
        >
          <div class={`${styles.radio} ${mode() === 'solo' ? styles.radioSelected : ''}`} />
          <div class={styles.modeIcon}>&#x1F4BB;</div>
          <h3 class={styles.modeTitle}>Solo</h3>
          <p class={styles.modeDescription}>
            Run LeapMux entirely on this machine. A Hub and Worker start together in
            a single process &mdash; no network setup required. Your data stays local.
            Ideal for personal use, local development, or trying out LeapMux.
          </p>
        </button>

        <button
          class={`${styles.modeCard} ${mode() === 'distributed' ? styles.modeCardSelected : ''}`}
          onClick={() => selectMode('distributed')}
        >
          <div class={`${styles.radio} ${mode() === 'distributed' ? styles.radioSelected : ''}`} />
          <div class={styles.modeIcon}>&#x1F310;</div>
          <h3 class={styles.modeTitle}>Distributed</h3>
          <p class={styles.modeDescription}>
            Connect to a remote LeapMux Hub shared across your team. Multiple workers
            can connect to a centralized hub for collaborative workflows. Requires a
            Hub server already running and accessible at the URL you provide.
          </p>
        </button>
      </div>

      <Show when={mode() === 'distributed'}>
        <div class={styles.section}>
          <label class={styles.label} for="hubUrl">Hub URL</label>
          <input
            id="hubUrl"
            class={styles.input}
            type="text"
            placeholder="https://hub.example.com"
            value={hubUrl()}
            onInput={(e) => {
              setHubUrl(e.currentTarget.value)
              setError('')
            }}
            autocomplete="off"
            autocorrect="off"
            autocapitalize="off"
            spellcheck={false}
          />
        </div>
      </Show>

      <Show when={mode() === 'solo' && !fdaGranted()}>
        <div class={styles.section}>
          <div class={styles.fdaCard}>
            <div class={styles.fdaHeader}>
              <span>&#x1F512;</span>
              <h4 class={styles.fdaTitle}>Full Disk Access Required</h4>
            </div>
            <div class={styles.fdaBody}>
              <p class={styles.fdaText}>
                Solo mode needs Full Disk Access to traverse directories and files in
                your home directory. Grant access in System Settings and the app will
                restart automatically.
              </p>
              <button
                class={styles.fdaButton}
                onClick={() => app().OpenFullDiskAccessSettings()}
              >
                Open System Settings
              </button>
            </div>
          </div>
        </div>
      </Show>

      <div class={styles.connectSection}>
        <button
          class={styles.connectBtn}
          disabled={!canConnect() || loading()}
          onClick={connect}
        >
          Connect
        </button>
        <Show when={loading()}>
          <div class={styles.spinner} />
        </Show>
        <Show when={error()}>
          <div class={styles.errorText}>{error()}</div>
        </Show>
      </div>

      <Show when={version()}>
        <div class={styles.versionText}>
          v
          {version()}
        </div>
      </Show>
    </div>
  )
}
