import type { Component } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, onCleanup, onMount } from 'solid-js'
import { getRuntimeState, platformBridge, restoreWindowGeometry } from '~/api/platformBridge'
import { Icon } from '~/components/common/Icon'
import { createLogger } from '~/lib/logger'
import { formatVersionLine } from '~/lib/systemInfo'
import { spinner } from '~/styles/animations.css'
import * as styles from './LauncherView.css'

const log = createLogger('launcher')

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
  const [versionLine, setVersionLine] = createSignal('')
  const [fdaGranted, setFdaGranted] = createSignal(true)
  const [visible, setVisible] = createSignal(false)
  let fdaPollTimer: ReturnType<typeof setInterval> | null = null
  let containerRef: HTMLDivElement | undefined

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
      const granted = await platformBridge.checkFullDiskAccess()
      const wasBlocked = !fdaGranted()
      setFdaGranted(granted)
      if (granted && wasBlocked) {
        stopFDAPoll()
        platformBridge.restart()
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
        await platformBridge.connectSolo()
        await fadeOut()
        props.onConnected()
      }
      else {
        await fadeOut()
        await platformBridge.connectDistributed(hubUrl().trim())
      }
    }
    catch (err) {
      setVisible(true)
      setError(err instanceof Error ? err.message : String(err))
      setLoading(false)
    }
  }

  onMount(async () => {
    try {
      const [, { config, buildInfo }] = await Promise.all([
        getRuntimeState(),
        platformBridge.getStartupInfo(),
      ])
      if (buildInfo.version)
        setVersionLine(formatVersionLine(buildInfo))

      // Restore saved window geometry on startup.
      await restoreWindowGeometry(config.window_width, config.window_height, config.window_maximized)

      if (config.mode === 'distributed' && config.hub_url) {
        setHubUrl(config.hub_url)
      }
      if (config.mode) {
        setMode(config.mode as 'solo' | 'distributed')
      }

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
    catch (err) {
      // Config load failed — show launcher anyway.
      log.error('failed to initialize launcher:', err)
      setVisible(true)
    }
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
          <div class={styles.modeHeader}>
            <span class={styles.modeIcon}>&#x1F4BB;</span>
            <h3 class={styles.modeTitle}>Solo</h3>
          </div>
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
          <div class={styles.modeHeader}>
            <span class={styles.modeIcon}>&#x1F310;</span>
            <h3 class={styles.modeTitle}>Distributed</h3>
          </div>
          <p class={styles.modeDescription}>
            Connect to a remote LeapMux Hub shared across your team. Multiple workers
            can connect to a centralized hub for collaborative workflows. Requires a
            Hub server already running and accessible at the URL you provide.
          </p>
        </button>
      </div>

      {/* Collapsible hub URL input — animated height via grid 0fr → 1fr */}
      <div class={`${styles.collapsible} ${mode() === 'distributed' ? styles.collapsibleVisible : ''}`}>
        <div class={styles.collapsibleInner}>
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
      </div>

      {/* Collapsible Full Disk Access notice */}
      <div class={`${styles.collapsible} ${mode() === 'solo' && !fdaGranted() ? styles.collapsibleVisible : ''}`}>
        <div class={styles.collapsibleInner}>
          <div class={styles.fdaCard}>
            <div class={styles.fdaHeader}>
              <span class={styles.fdaIcon}>&#x1F512;</span>
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
                onClick={() => platformBridge.openFullDiskAccessSettings()}
              >
                Open System Settings
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class={styles.connectSection}>
        <button
          class={styles.connectBtn}
          disabled={!canConnect() || loading()}
          onClick={connect}
        >
          {loading() && <Icon icon={LoaderCircle} size="sm" class={spinner} />}
          Connect
        </button>
        {/* Collapsible error message */}
        <div class={`${styles.collapsible} ${error() ? styles.collapsibleVisible : ''}`} style={{ 'margin-top': 0 }}>
          <div class={styles.collapsibleInner}>
            <div class={styles.errorText}>{error()}</div>
          </div>
        </div>
      </div>

      <div class={styles.versionText}>
        {versionLine() || '\u00A0'}
      </div>
    </div>
  )
}
