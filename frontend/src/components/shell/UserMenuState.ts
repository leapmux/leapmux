import { createSignal } from 'solid-js'

const [showProfileDialog, setShowProfileDialog] = createSignal(false)
const [showPreferencesDialog, setShowPreferencesDialog] = createSignal(false)
const [showAboutDialog, setShowAboutDialog] = createSignal(false)

export {
  setShowAboutDialog,
  setShowPreferencesDialog,
  setShowProfileDialog,
  showAboutDialog,
  showPreferencesDialog,
  showProfileDialog,
}
