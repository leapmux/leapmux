import { useNavigate } from '@solidjs/router'
import { createEffect } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { isSoloMode } from '~/lib/systemInfo'

export default function IndexRoute() {
  const auth = useAuth()
  const navigate = useNavigate()

  createEffect(() => {
    if (auth.loading())
      return

    const user = auth.user()
    if (user) {
      navigate(`/o/${user.username}`, { replace: true })
    }
    else if (!isSoloMode()) {
      navigate('/login', { replace: true })
    }
  })

  return null
}
