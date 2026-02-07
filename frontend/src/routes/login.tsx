import { onMount } from 'solid-js'
import { LoginPage } from '~/components/common/LoginPage'

export default function LoginRoute() {
  onMount(() => {
    document.title = 'Login - LeapMux'
  })
  return <LoginPage />
}
