import { onMount } from 'solid-js'
import { SignupPage } from '~/components/common/SignupPage'

export default function SignupRoute() {
  onMount(() => {
    document.title = 'Sign Up - LeapMux'
  })
  return <SignupPage />
}
