import type { Component } from 'solid-js'
import { createSignal, For, onMount, Show } from 'solid-js'
import { adminClient } from '~/api/clients'
import { loadTimeouts } from '~/api/transport'
import * as styles from './AdminSettingsPage.css'

export const AdminSystemSettings: Component = () => {
  // --- System Settings state ---
  const [signupEnabled, setSignupEnabled] = createSignal(false)
  const [emailVerificationRequired, setEmailVerificationRequired] = createSignal(false)
  const [smtpHost, setSmtpHost] = createSignal('')
  const [smtpPort, setSmtpPort] = createSignal(587)
  const [smtpUsername, setSmtpUsername] = createSignal('')
  const [smtpPassword, setSmtpPassword] = createSignal('')
  const [smtpPasswordSet, setSmtpPasswordSet] = createSignal(false)
  const [smtpFromAddress, setSmtpFromAddress] = createSignal('')
  const [smtpUseTls, setSmtpUseTls] = createSignal(true)
  const [apiTimeout, setApiTimeout] = createSignal(10)
  const [agentStartupTimeout, setAgentStartupTimeout] = createSignal(30)
  const [worktreeCreateTimeout, setWorktreeCreateTimeout] = createSignal(60)
  const [settingsSaving, setSettingsSaving] = createSignal(false)
  const [settingsMessage, setSettingsMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // --- Log Level state ---
  const logLevels = ['DEBUG', 'INFO', 'WARN', 'ERROR']
  const [logLevel, setLogLevel] = createSignal('INFO')
  const [logLevelMessage, setLogLevelMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const loadSettings = async () => {
    try {
      const resp = await adminClient.getSettings({})
      const s = resp.settings
      if (s) {
        setSignupEnabled(s.signupEnabled)
        setEmailVerificationRequired(s.emailVerificationRequired)
        if (s.smtp) {
          setSmtpHost(s.smtp.host)
          setSmtpPort(s.smtp.port)
          setSmtpUsername(s.smtp.username)
          setSmtpPasswordSet(s.smtp.passwordSet)
          setSmtpFromAddress(s.smtp.fromAddress)
          setSmtpUseTls(s.smtp.useTls)
        }
        if (s.apiTimeoutSeconds > 0)
          setApiTimeout(s.apiTimeoutSeconds)
        if (s.agentStartupTimeoutSeconds > 0)
          setAgentStartupTimeout(s.agentStartupTimeoutSeconds)
        if (s.worktreeCreateTimeoutSeconds > 0)
          setWorktreeCreateTimeout(s.worktreeCreateTimeoutSeconds)
      }
    }
    catch (e) {
      setSettingsMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to load settings' })
    }
  }

  const handleSaveSettings = async () => {
    setSettingsSaving(true)
    setSettingsMessage(null)
    try {
      await adminClient.updateSettings({
        settings: {
          signupEnabled: signupEnabled(),
          emailVerificationRequired: emailVerificationRequired(),
          smtp: {
            host: smtpHost(),
            port: smtpPort(),
            username: smtpUsername(),
            password: smtpPassword(),
            passwordSet: smtpPasswordSet(),
            fromAddress: smtpFromAddress(),
            useTls: smtpUseTls(),
          },
          apiTimeoutSeconds: apiTimeout(),
          agentStartupTimeoutSeconds: agentStartupTimeout(),
          worktreeCreateTimeoutSeconds: worktreeCreateTimeout(),
        },
      })
      setSettingsMessage({ type: 'success', text: 'Settings saved.' })
      // Refresh the in-memory timeout config for this browser session.
      loadTimeouts().catch(() => {})
      // Clear password field after save and refresh password-set status
      setSmtpPassword('')
      if (smtpPassword()) {
        setSmtpPasswordSet(true)
      }
    }
    catch (e) {
      setSettingsMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to save settings' })
    }
    finally {
      setSettingsSaving(false)
    }
  }

  const loadLogLevel = async () => {
    try {
      const resp = await adminClient.getLogLevel({})
      setLogLevel(resp.level || 'INFO')
    }
    catch (e) {
      setLogLevelMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to load log level' })
    }
  }

  const handleSetLogLevel = async (level: string) => {
    setLogLevelMessage(null)
    try {
      const resp = await adminClient.setLogLevel({ level })
      setLogLevel(resp.level || level)
      setLogLevelMessage({ type: 'success', text: `Log level set to ${resp.level}.` })
    }
    catch (e) {
      setLogLevelMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to set log level' })
    }
  }

  onMount(() => {
    loadSettings()
    loadLogLevel()
  })

  return (
    <>
      {/* ===== System Settings ===== */}
      <div class={styles.section}>
        <h2>System Settings</h2>
        <div class="vstack gap-4">
          <label class={styles.toggleRow}>
            <span class={styles.toggleLabel}>Sign-up enabled</span>
            <input type="checkbox" role="switch" checked={signupEnabled()} onChange={e => setSignupEnabled(e.currentTarget.checked)} />
          </label>

          <Show when={signupEnabled()}>
            <label class={styles.toggleRow}>
              <span class={styles.toggleLabel}>Email verification required</span>
              <input type="checkbox" role="switch" checked={emailVerificationRequired()} onChange={e => setEmailVerificationRequired(e.currentTarget.checked)} />
            </label>
          </Show>

          <div class={styles.subsection}>
            <h3>SMTP Configuration</h3>
            <div class="vstack gap-4">
              <label class={styles.fieldLabel}>
                Host
                <input value={smtpHost()} onInput={e => setSmtpHost(e.currentTarget.value)} placeholder="smtp.example.com" />
              </label>
              <label class={styles.fieldLabel}>
                Port
                <input type="number" value={String(smtpPort())} onInput={e => setSmtpPort(Number.parseInt(e.currentTarget.value) || 0)} />
              </label>
              <label class={styles.fieldLabel}>
                Username
                <input value={smtpUsername()} onInput={e => setSmtpUsername(e.currentTarget.value)} />
              </label>
              <label class={styles.fieldLabel}>
                Password
                <Show when={smtpPasswordSet()}>
                  <span class={styles.passwordSetIndicator}>Password set</span>
                </Show>
                <input
                  type="password"
                  value={smtpPassword()}
                  onInput={e => setSmtpPassword(e.currentTarget.value)}
                  placeholder={smtpPasswordSet() ? 'Leave blank to keep current' : 'Enter password'}
                />
              </label>
              <label class={styles.fieldLabel}>
                From Address
                <input type="email" value={smtpFromAddress()} onInput={e => setSmtpFromAddress(e.currentTarget.value)} placeholder="noreply@example.com" />
              </label>
              <label class={styles.toggleRow}>
                <span class={styles.toggleLabel}>Use TLS</span>
                <input type="checkbox" role="switch" checked={smtpUseTls()} onChange={e => setSmtpUseTls(e.currentTarget.checked)} />
              </label>
            </div>
          </div>

          <div class={styles.subsection}>
            <h3>Timeouts</h3>
            <div class="vstack gap-4">
              <label class={styles.fieldLabel}>
                API Timeout (seconds)
                <input type="number" min="1" max="600" value={String(apiTimeout())} onInput={e => setApiTimeout(Number.parseInt(e.currentTarget.value) || 10)} />
              </label>
              <label class={styles.fieldLabel}>
                Agent Startup Timeout (seconds)
                <input type="number" min="1" max="600" value={String(agentStartupTimeout())} onInput={e => setAgentStartupTimeout(Number.parseInt(e.currentTarget.value) || 30)} />
              </label>
              <label class={styles.fieldLabel}>
                Worktree Create Timeout (seconds)
                <input type="number" min="1" max="600" value={String(worktreeCreateTimeout())} onInput={e => setWorktreeCreateTimeout(Number.parseInt(e.currentTarget.value) || 60)} />
              </label>
            </div>
          </div>

          <Show when={settingsMessage()}>
            {msg => (
              <div role="alert" class={msg().type === 'success' ? styles.successText : styles.errorText}>
                {msg().text}
              </div>
            )}
          </Show>

          <button
            onClick={handleSaveSettings}
            disabled={settingsSaving()}
          >
            {settingsSaving() ? 'Saving...' : 'Save Settings'}
          </button>
        </div>
      </div>

      {/* ===== Log Level ===== */}
      <div class={styles.section}>
        <h2>Hub Log Level</h2>
        <div class="vstack gap-4">
          <div>
            <label class={styles.toggleLabel}>Level</label>
            <select
              value={logLevel()}
              onChange={(e) => {
                const v = e.currentTarget.value
                if (v)
                  handleSetLogLevel(v)
              }}
            >
              <For each={logLevels}>
                {level => <option value={level}>{level}</option>}
              </For>
            </select>
          </div>

          <Show when={logLevelMessage()}>
            {msg => (
              <div role="alert" class={msg().type === 'success' ? styles.successText : styles.errorText}>
                {msg().text}
              </div>
            )}
          </Show>
        </div>
      </div>
    </>
  )
}
