import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { isSoloMode } from '~/lib/systemInfo'
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
        <Show
          when={!isSoloMode()}
          fallback={(
            <>
              <AccountAppearanceSettings />
              <FontSettings />
            </>
          )}
        >
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
        </Show>
      </section>
    </Dialog>
  )
}
