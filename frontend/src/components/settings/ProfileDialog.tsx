import type { Component } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { ProfileSettings } from './ProfileSettings'

interface ProfileDialogProps {
  onClose: () => void
}

export const ProfileDialog: Component<ProfileDialogProps> = (props) => {
  return (
    <Dialog title="Profile" tall onClose={props.onClose}>
      <section>
        <ProfileSettings />
      </section>
    </Dialog>
  )
}
