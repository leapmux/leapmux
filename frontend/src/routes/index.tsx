import { useNavigate } from '@solidjs/router'
import { onMount } from 'solid-js'
import { useAuth } from '~/context/AuthContext'

export default function IndexRoute() {
  const auth = useAuth()
  const navigate = useNavigate()

  onMount(() => {
    const user = auth.user()
    if (user) {
      navigate(`/o/${user.username}`, { replace: true })
    }
    else {
      navigate('/login', { replace: true })
    }
  })

  return null
}
