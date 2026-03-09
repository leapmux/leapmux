import type { Component } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { AdminUserManagement } from './AdminUserManagement'

interface AdminDialogProps {
  onClose: () => void
}

export const AdminDialog: Component<AdminDialogProps> = (props) => {
  return (
    <Dialog title="Administration" tall onClose={props.onClose}>
      <section>
        <AdminUserManagement />
      </section>
    </Dialog>
  )
}
