import type { Component } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { AccountAppearanceSettings, BrowserAppearanceSettings } from './AppearanceSettings'
import { FontSettings } from './FontSettings'
import { PasswordSettings } from './PasswordSettings'
import { ProfileSettings } from './ProfileSettings'

interface PreferencesDialogProps {
  onClose: () => void
}

export const PreferencesDialog: Component<PreferencesDialogProps> = (props) => {
  return (
    <Dialog title="Preferences" tall onClose={props.onClose}>
      <section>
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
      </section>
    </Dialog>
  )
}
