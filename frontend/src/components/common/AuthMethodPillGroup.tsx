import type { PillOptions } from './PillGroup'
import type { AuthMethod, AuthMethodSelection } from '~/lib/authMethodSelection'
import { passkeyBlocker } from '~/lib/systemInfo'
import { passkeyBlockerMessage } from '~/lib/webauthn'
import { PillGroup } from './PillGroup'

function authMethodOptions(): PillOptions<AuthMethod> {
  const password = { key: 'password' as const, label: 'Password' }
  const blocker = passkeyBlocker()
  if (blocker === 'origin-not-allowed')
    return [password]
  return [
    password,
    {
      key: 'passkey',
      label: 'Passkey',
      disabledReason: blocker ? passkeyBlockerMessage(blocker) : undefined,
    },
  ]
}

/** The shared authentication-method control for each credential form. */
export function AuthMethodPillGroup(props: {
  label: string
  selection: AuthMethodSelection
}) {
  return (
    <PillGroup
      label={props.label}
      options={authMethodOptions()}
      selectedKey={props.selection.effectiveMethod()}
      onSelect={props.selection.select}
    />
  )
}
