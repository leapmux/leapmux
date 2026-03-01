import type { Component } from 'solid-js'
import { A } from '@solidjs/router'
import { useAuth } from '~/context/AuthContext'
import * as styles from './AdminSettingsPage.css'
import { AdminSystemSettings } from './AdminSystemSettings'
import { AdminUserManagement } from './AdminUserManagement'

export const AdminSettingsPage: Component = () => {
  const auth = useAuth()

  return (
    <div class={styles.pageContainer}>
      <A href={`/o/${auth.user()?.username || ''}`} class={styles.backLink}>&larr; Dashboard</A>
      <h1>Administration</h1>

      <AdminSystemSettings />
      <AdminUserManagement />
    </div>
  )
}
