import type { ParentComponent } from 'solid-js'
import { useLocation, useNavigate } from '@solidjs/router'
import { createEffect, createMemo, Show } from 'solid-js'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { useAuth } from '~/context/AuthContext'
import { centeredFull } from '~/styles/shared.css'

interface AuthGuardProps {
  requireAdmin?: boolean
}

export const AuthGuard: ParentComponent<AuthGuardProps> = (props) => {
  const auth = useAuth()
  const location = useLocation()
  const navigate = useNavigate()

  createEffect(() => {
    if (auth.loading())
      return

    if (!auth.isAuthenticated()) {
      const returnTo = location.pathname + location.search
      navigate(`/login?redirect=${encodeURIComponent(returnTo)}`, { replace: true })
    }
  })

  const showNotFound = createMemo(() =>
    props.requireAdmin && !auth.loading() && auth.isAuthenticated() && !auth.user()?.isAdmin,
  )

  return (
    <Show
      when={!auth.loading() && auth.isAuthenticated()}
      fallback={<div class={centeredFull}>Loading...</div>}
    >
      <Show
        when={!showNotFound()}
        fallback={<NotFoundPage linkHref="/" linkText="Go to Dashboard" />}
      >
        {props.children}
      </Show>
    </Show>
  )
}
