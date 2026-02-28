import type { Component } from 'solid-js'
import { A } from '@solidjs/router'
import { useAuth } from '~/context/AuthContext'
import { AccountAppearanceSettings, BrowserAppearanceSettings } from './AppearanceSettings'
import { FontSettings } from './FontSettings'
import { PasswordSettings } from './PasswordSettings'
import * as styles from './PreferencesPage.css'
import { ProfileSettings } from './ProfileSettings'

export const PreferencesPage: Component = () => {
  const auth = useAuth()

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
          <BrowserAppearanceSettings />
        </div>

        {/* ===== ACCOUNT DEFAULTS TAB ===== */}
        <div role="tabpanel">
          <AccountAppearanceSettings />
          <FontSettings />
          <ProfileSettings />
          <PasswordSettings />
        </div>
      </ot-tabs>
    </div>
  )
}
