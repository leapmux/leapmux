import type { Component } from 'solid-js'
import type { ThemePreference } from '~/app'
import type { DiffViewPreference, TurnEndSoundPreference } from '~/context/PreferencesContext'
import type { TerminalThemePreference } from '~/lib/terminal'
import { A } from '@solidjs/router'
import { createSignal, For, onMount, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { usePreferences } from '~/context/PreferencesContext'
import { sanitizeName, sanitizeSlug } from '~/lib/validate'
import * as styles from './PreferencesPage.css'

export const PreferencesPage: Component = () => {
  const auth = useAuth()
  const prefs = usePreferences()

  // Profile fields
  const [username, setUsername] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [email, setEmail] = createSignal('')
  const [profileSaving, setProfileSaving] = createSignal(false)
  const [profileMessage, setProfileMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // Password fields
  const [currentPassword, setCurrentPassword] = createSignal('')
  const [newPassword, setNewPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [passwordSaving, setPasswordSaving] = createSignal(false)
  const [passwordMessage, setPasswordMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // Font editing (account-level)
  const [uiFonts, setUiFonts] = createSignal<string[]>([])
  const [monoFonts, setMonoFonts] = createSignal<string[]>([])
  const [fontSaving, setFontSaving] = createSignal(false)
  const [fontMessage, setFontMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)
  const [newUiFont, setNewUiFont] = createSignal('')
  const [newMonoFont, setNewMonoFont] = createSignal('')

  // Inline edit state
  const [editingKey, setEditingKey] = createSignal<string | null>(null)
  const [editingValue, setEditingValue] = createSignal('')
  const [editingError, setEditingError] = createSignal<string | null>(null)
  let editCancelled = false

  // Drag state
  const [dragIndex, setDragIndex] = createSignal<number | null>(null)
  const [dragList, setDragList] = createSignal<'ui' | 'mono' | null>(null)

  onMount(() => {
    const user = auth.user()
    if (user) {
      setUsername(user.username)
      setDisplayName(user.displayName)
      setEmail(user.email)
    }
    // Init local font copies from context
    setUiFonts([...prefs.uiFonts()])
    setMonoFonts([...prefs.monoFonts()])
  })

  // --- Profile ---
  const handleSaveProfile = async () => {
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      setProfileMessage({ type: 'error', text: slugErr })
      return
    }
    setProfileSaving(true)
    setProfileMessage(null)
    try {
      const { updateProfile } = await import('~/api/clients').then(m => ({ updateProfile: m.userClient.updateProfile }))
      await updateProfile({
        username: slug,
        displayName: displayName(),
        email: email(),
      })
      setProfileMessage({ type: 'success', text: 'Profile updated.' })
    }
    catch (e) {
      setProfileMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to update profile' })
    }
    finally {
      setProfileSaving(false)
    }
  }

  // --- Password ---
  const handleChangePassword = async () => {
    if (newPassword() !== confirmPassword()) {
      setPasswordMessage({ type: 'error', text: 'Passwords do not match.' })
      return
    }
    setPasswordSaving(true)
    setPasswordMessage(null)
    try {
      const { changePassword } = await import('~/api/clients').then(m => ({ changePassword: m.userClient.changePassword }))
      await changePassword({
        currentPassword: currentPassword(),
        newPassword: newPassword(),
      })
      setPasswordMessage({ type: 'success', text: 'Password changed.' })
      setCurrentPassword('')
      setNewPassword('')
      setConfirmPassword('')
    }
    catch (e) {
      setPasswordMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to change password' })
    }
    finally {
      setPasswordSaving(false)
    }
  }

  // --- Account font preferences ---
  const saveAccountFonts = async (uiEnabled: boolean, monoEnabled: boolean, ui: string[], mono: string[]) => {
    setFontSaving(true)
    setFontMessage(null)
    try {
      prefs.setAccountUiFontCustomEnabled(uiEnabled)
      prefs.setAccountMonoFontCustomEnabled(monoEnabled)
      prefs.setUiFonts(ui)
      prefs.setMonoFonts(mono)
      await prefs.saveAccountPreferences()
      setFontMessage({ type: 'success', text: 'Font preferences saved.' })
    }
    catch (e) {
      setFontMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to save font preferences' })
    }
    finally {
      setFontSaving(false)
    }
  }

  const handleToggleUiFonts = (enabled: boolean) => {
    saveAccountFonts(enabled, prefs.accountMonoFontCustomEnabled(), uiFonts(), monoFonts())
  }

  const handleToggleMonoFonts = (enabled: boolean) => {
    saveAccountFonts(prefs.accountUiFontCustomEnabled(), enabled, uiFonts(), monoFonts())
  }

  const addFont = (list: 'ui' | 'mono') => {
    const name = list === 'ui' ? newUiFont() : newMonoFont()
    const { value: sanitized, error } = sanitizeName(name)
    if (error) {
      setFontMessage({ type: 'error', text: error })
      return
    }
    if (list === 'ui') {
      const updated = [...uiFonts(), sanitized]
      setUiFonts(updated)
      setNewUiFont('')
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), updated, monoFonts())
    }
    else {
      const updated = [...monoFonts(), sanitized]
      setMonoFonts(updated)
      setNewMonoFont('')
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), updated)
    }
  }

  const removeFont = (list: 'ui' | 'mono', index: number) => {
    if (list === 'ui') {
      const updated = uiFonts().filter((_, i) => i !== index)
      setUiFonts(updated)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), updated, monoFonts())
    }
    else {
      const updated = monoFonts().filter((_, i) => i !== index)
      setMonoFonts(updated)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), updated)
    }
  }

  const handleDragStart = (list: 'ui' | 'mono', index: number) => {
    setDragIndex(index)
    setDragList(list)
  }

  const handleDragOver = (e: DragEvent) => {
    e.preventDefault()
  }

  const handleDrop = (list: 'ui' | 'mono', targetIndex: number) => {
    const srcIndex = dragIndex()
    const srcList = dragList()
    if (srcIndex === null || srcList !== list)
      return
    if (srcIndex === targetIndex)
      return

    const fonts = list === 'ui' ? [...uiFonts()] : [...monoFonts()]
    const [moved] = fonts.splice(srcIndex, 1)
    fonts.splice(targetIndex, 0, moved)

    if (list === 'ui') {
      setUiFonts(fonts)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), fonts, monoFonts())
    }
    else {
      setMonoFonts(fonts)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), fonts)
    }

    setDragIndex(null)
    setDragList(null)
  }

  const handleDragEnd = () => {
    setDragIndex(null)
    setDragList(null)
  }

  // --- Account theme ---
  const handleAccountThemeChange = async (newTheme: ThemePreference) => {
    prefs.setAccountTheme(newTheme)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountTerminalThemeChange = async (newTheme: TerminalThemePreference) => {
    prefs.setAccountTerminalTheme(newTheme)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountDiffViewChange = async (newDiffView: DiffViewPreference) => {
    prefs.setAccountDiffView(newDiffView)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountTurnEndSoundChange = async (newSound: TurnEndSoundPreference) => {
    prefs.setAccountTurnEndSound(newSound)
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  const handleAccountTurnEndSoundVolumeChangeEnd = async () => {
    try {
      await prefs.saveAccountPreferences()
    }
    catch {
      // Best effort
    }
  }

  // --- Inline font editing ---
  const startFontEdit = (list: 'ui' | 'mono', index: number, currentName: string) => {
    setEditingKey(`${list}-${index}`)
    setEditingValue(currentName)
    setEditingError(null)
  }

  const commitFontEdit = (list: 'ui' | 'mono', index: number) => {
    if (editCancelled) {
      editCancelled = false
      return
    }
    const { value, error } = sanitizeName(editingValue())
    if (error) {
      setEditingError(error)
      return
    }
    const fonts = list === 'ui' ? [...uiFonts()] : [...monoFonts()]
    if (value !== fonts[index]) {
      fonts[index] = value
      if (list === 'ui') {
        setUiFonts(fonts)
        saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), fonts, monoFonts())
      }
      else {
        setMonoFonts(fonts)
        saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), fonts)
      }
    }
    setEditingKey(null)
    setEditingError(null)
  }

  const cancelFontEdit = () => {
    editCancelled = true
    setEditingKey(null)
    setEditingError(null)
  }

  // --- Helpers ---
  const renderThemeButtons = (
    current: () => string,
    onChange: (v: string) => void,
    options: { value: string, label: string }[],
  ) => (
    <div class={styles.pillGroup}>
      <For each={options}>
        {opt => (
          <button
            class={current() === opt.value ? styles.pillOptionActive : styles.pillOption}
            onClick={() => onChange(opt.value)}
          >
            {opt.label}
          </button>
        )}
      </For>
    </div>
  )

  const renderFontList = (label: string, list: 'ui' | 'mono', fonts: () => string[]) => {
    const inputValue = () => list === 'ui' ? newUiFont() : newMonoFont()
    const setInputValue = (v: string) => list === 'ui' ? setNewUiFont(v) : setNewMonoFont(v)

    return (
      <div class="vstack gap-4">
        <span class={styles.fieldLabel}>{label}</span>
        <Show
          when={fonts().length > 0}
          fallback={<div class={styles.fontListEmpty}>No fonts configured</div>}
        >
          <div class={styles.fontList}>
            <For each={fonts()}>
              {(font, i) => (
                <div
                  class={styles.fontListItem}
                  draggable={editingKey() !== `${list}-${i()}`}
                  onDragStart={() => handleDragStart(list, i())}
                  onDragOver={handleDragOver}
                  onDrop={() => handleDrop(list, i())}
                  onDragEnd={handleDragEnd}
                >
                  <span class={styles.fontDragHandle}>&#x283F;</span>
                  <Show
                    when={editingKey() === `${list}-${i()}`}
                    fallback={(
                      <span
                        class={styles.fontName}
                        onDblClick={() => startFontEdit(list, i(), font)}
                      >
                        {font}
                      </span>
                    )}
                  >
                    <div class={styles.fontEditWrapper}>
                      <input
                        class={styles.fontEditInput}
                        type="text"
                        value={editingValue()}
                        onInput={(e) => {
                          const { value, error } = sanitizeName(e.currentTarget.value)
                          setEditingValue(value)
                          setEditingError(error)
                        }}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') {
                            commitFontEdit(list, i())
                          }
                          else if (e.key === 'Escape') {
                            cancelFontEdit()
                          }
                        }}
                        onBlur={() => commitFontEdit(list, i())}
                        ref={(el) => {
                          requestAnimationFrame(() => {
                            el.focus()
                            el.select()
                          })
                        }}
                      />
                      <Show when={editingError()}>
                        <span class={styles.fontEditError}>{editingError()}</span>
                      </Show>
                    </div>
                  </Show>
                  <button
                    class={styles.fontRemoveButton}
                    onClick={() => removeFont(list, i())}
                    title="Remove"
                  >
                    &#xd7;
                  </button>
                </div>
              )}
            </For>
          </div>
        </Show>
        <div class={styles.fontAddRow}>
          <input
            type="text"
            placeholder="Font name"
            value={inputValue()}
            onInput={e => setInputValue(sanitizeName(e.currentTarget.value).value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                addFont(list)
              }
            }}
            disabled={fontSaving()}
          />
          <button
            class="small outline"
            onClick={() => addFont(list)}
            disabled={fontSaving() || !inputValue().trim()}
            title={`Add ${label.toLowerCase()}`}
          >
            +
          </button>
        </div>
      </div>
    )
  }

  const themeOptions = [
    { value: 'dark', label: 'Dark' },
    { value: 'light', label: 'Light' },
    { value: 'system', label: 'System' },
  ]

  const terminalThemeOptions = [
    { value: 'match-ui', label: 'Match UI' },
    { value: 'dark', label: 'Dark' },
    { value: 'light', label: 'Light' },
  ]

  const diffViewOptions = [
    { value: 'unified', label: 'Unified' },
    { value: 'split', label: 'Side-by-Side' },
  ]

  const turnEndSoundOptions = [
    { value: 'none', label: 'None' },
    { value: 'ding-dong', label: 'Ding Dong' },
  ]

  return (
    <div class={styles.container}>
      <A href={`/o/${auth.user()?.username || ''}`} class={styles.backLink}>&larr; Dashboard</A>
      <h1>Preferences</h1>

      <ot-tabs>
        <nav role="tablist">
          <button role="tab">This Browser</button>
          <button role="tab">Account Defaults</button>
        </nav>

        {/* ===== THIS BROWSER TAB ===== */}
        <div role="tabpanel">
          <div class={styles.section}>
            <h2>Theme</h2>
            <div class={styles.pillGroup}>
              <button
                class={prefs.browserTheme() === null ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserTheme(null)}
              >
                Use account default
              </button>
              <For each={themeOptions}>
                {opt => (
                  <button
                    class={prefs.browserTheme() === opt.value ? styles.pillOptionActive : styles.pillOption}
                    onClick={() => prefs.setBrowserTheme(opt.value as ThemePreference)}
                  >
                    {opt.label}
                  </button>
                )}
              </For>
            </div>
          </div>

          <div class={styles.section}>
            <h2>Terminal Theme</h2>
            <div class={styles.pillGroup}>
              <button
                class={prefs.browserTerminalTheme() === null ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserTerminalTheme(null)}
              >
                Use account default
              </button>
              <For each={terminalThemeOptions}>
                {opt => (
                  <button
                    class={prefs.browserTerminalTheme() === opt.value ? styles.pillOptionActive : styles.pillOption}
                    onClick={() => prefs.setBrowserTerminalTheme(opt.value as TerminalThemePreference)}
                  >
                    {opt.label}
                  </button>
                )}
              </For>
            </div>
          </div>

          <div class={styles.section}>
            <h2>Diff View</h2>
            <div class={styles.pillGroup}>
              <button
                class={prefs.browserDiffView() === null ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserDiffView(null)}
              >
                Use account default
              </button>
              <For each={diffViewOptions}>
                {opt => (
                  <button
                    class={prefs.browserDiffView() === opt.value ? styles.pillOptionActive : styles.pillOption}
                    onClick={() => prefs.setBrowserDiffView(opt.value as DiffViewPreference)}
                  >
                    {opt.label}
                  </button>
                )}
              </For>
            </div>
          </div>

          <div class={styles.section}>
            <h2>Turn End Sound</h2>
            <div class={styles.pillGroup}>
              <button
                class={prefs.browserTurnEndSound() === null ? styles.pillOptionActive : styles.pillOption}
                onClick={() => prefs.setBrowserTurnEndSound(null)}
              >
                Use account default
              </button>
              <For each={turnEndSoundOptions}>
                {opt => (
                  <button
                    class={prefs.browserTurnEndSound() === opt.value ? styles.pillOptionActive : styles.pillOption}
                    onClick={() => prefs.setBrowserTurnEndSound(opt.value as TurnEndSoundPreference)}
                  >
                    {opt.label}
                  </button>
                )}
              </For>
            </div>
            <Show when={prefs.turnEndSound() !== 'none'}>
              <div class={styles.sliderGroup}>
                <div class={styles.volumeOverrideRow}>
                  <span class={styles.fieldLabel}>Volume</span>
                  <button
                    aria-pressed={prefs.browserTurnEndSoundVolume() !== null}
                    onClick={() => {
                      const pressed = !(prefs.browserTurnEndSoundVolume() !== null)
                      if (pressed) {
                        prefs.setBrowserTurnEndSoundVolume(prefs.accountTurnEndSoundVolume())
                      }
                      else {
                        prefs.setBrowserTurnEndSoundVolume(null)
                      }
                    }}
                    class={prefs.browserTurnEndSoundVolume() !== null
                      ? styles.pillOptionActive
                      : styles.pillOption}
                  >
                    {prefs.browserTurnEndSoundVolume() !== null ? 'Custom volume' : 'Use account default'}
                  </button>
                </div>
                <Show when={prefs.browserTurnEndSoundVolume() !== null}>
                  <div class={styles.sliderRow}>
                    <input type="range" min={0} max={100} step={1} value={prefs.browserTurnEndSoundVolume()!} onInput={e => prefs.setBrowserTurnEndSoundVolume(Number(e.currentTarget.value))} />
                    <span class={styles.sliderValue}>
                      {prefs.browserTurnEndSoundVolume()}
                      %
                    </span>
                  </div>
                </Show>
              </div>
            </Show>
          </div>
        </div>

        {/* ===== ACCOUNT DEFAULTS TAB ===== */}
        <div role="tabpanel">
          <div class={styles.section}>
            <h2>Theme</h2>
            {renderThemeButtons(
              () => prefs.accountTheme(),
              v => handleAccountThemeChange(v as ThemePreference),
              themeOptions,
            )}
          </div>

          <div class={styles.section}>
            <h2>Terminal Theme</h2>
            {renderThemeButtons(
              () => prefs.accountTerminalTheme(),
              v => handleAccountTerminalThemeChange(v as TerminalThemePreference),
              terminalThemeOptions,
            )}
          </div>

          <div class={styles.section}>
            <h2>Diff View</h2>
            {renderThemeButtons(
              () => prefs.accountDiffView(),
              v => handleAccountDiffViewChange(v as DiffViewPreference),
              diffViewOptions,
            )}
          </div>

          <div class={styles.section}>
            <h2>Turn End Sound</h2>
            {renderThemeButtons(
              () => prefs.accountTurnEndSound(),
              v => handleAccountTurnEndSoundChange(v as TurnEndSoundPreference),
              turnEndSoundOptions,
            )}
            <Show when={prefs.accountTurnEndSound() !== 'none'}>
              <div class={styles.sliderGroup}>
                <span class={styles.fieldLabel}>Volume</span>
                <div class={styles.sliderRow}>
                  <input type="range" min={0} max={100} step={1} value={prefs.accountTurnEndSoundVolume()} onInput={e => prefs.setAccountTurnEndSoundVolume(Number(e.currentTarget.value))} onChange={handleAccountTurnEndSoundVolumeChangeEnd} />
                  <span class={styles.sliderValue}>
                    {prefs.accountTurnEndSoundVolume()}
                    %
                  </span>
                </div>
              </div>
            </Show>
          </div>

          <div class={styles.section}>
            <h2>Fonts</h2>
            <div class="vstack gap-4">
              <label class={styles.toggleLabel}>
                Custom UI fonts
                <input type="checkbox" role="switch" checked={prefs.accountUiFontCustomEnabled()} onChange={e => handleToggleUiFonts(e.currentTarget.checked)} />
              </label>

              <label class={styles.toggleLabel}>
                Custom monospace fonts
                <input type="checkbox" role="switch" checked={prefs.accountMonoFontCustomEnabled()} onChange={e => handleToggleMonoFonts(e.currentTarget.checked)} />
              </label>
            </div>

            <Show when={prefs.accountUiFontCustomEnabled()}>
              {renderFontList('UI Fonts', 'ui', uiFonts)}
            </Show>
            <Show when={prefs.accountMonoFontCustomEnabled()}>
              {renderFontList('Monospace Fonts', 'mono', monoFonts)}
            </Show>

            <Show when={fontMessage()}>
              {msg => <div class={msg().type === 'success' ? styles.successText : styles.errorText}>{msg().text}</div>}
            </Show>
          </div>

          <div class={styles.section}>
            <h2>Profile</h2>
            <div class="vstack gap-4">
              <label class={styles.fieldLabel}>
                Username
                <input type="text" value={username()} onInput={e => setUsername(e.currentTarget.value)} />
              </label>
              <Show when={username() !== auth.user()?.username}>
                <div class={styles.warningText}>Changing your username will also rename your personal organization.</div>
              </Show>
              <label class={styles.fieldLabel}>
                Display Name
                <input type="text" value={displayName()} onInput={e => setDisplayName(e.currentTarget.value)} />
              </label>
              <label class={styles.fieldLabel}>
                Email
                <input type="email" value={email()} onInput={e => setEmail(e.currentTarget.value)} />
              </label>
              <Show when={profileMessage()}>
                {msg => <div class={msg().type === 'success' ? styles.successText : styles.errorText}>{msg().text}</div>}
              </Show>
              <button onClick={handleSaveProfile} disabled={profileSaving()}>
                {profileSaving() ? 'Saving...' : 'Save Profile'}
              </button>
            </div>
          </div>

          <div class={styles.section}>
            <h2>Change Password</h2>
            <div class="vstack gap-4">
              <label class={styles.fieldLabel}>
                Current Password
                <input type="password" value={currentPassword()} onInput={e => setCurrentPassword(e.currentTarget.value)} />
              </label>
              <label class={styles.fieldLabel}>
                New Password
                <input type="password" value={newPassword()} onInput={e => setNewPassword(e.currentTarget.value)} />
              </label>
              <label class={styles.fieldLabel}>
                Confirm New Password
                <input type="password" value={confirmPassword()} onInput={e => setConfirmPassword(e.currentTarget.value)} />
              </label>
              <Show when={passwordMessage()}>
                {msg => <div class={msg().type === 'success' ? styles.successText : styles.errorText}>{msg().text}</div>}
              </Show>
              <button onClick={handleChangePassword} disabled={passwordSaving() || !currentPassword() || !newPassword()}>
                {passwordSaving() ? 'Changing...' : 'Change Password'}
              </button>
            </div>
          </div>
        </div>
      </ot-tabs>
    </div>
  )
}
