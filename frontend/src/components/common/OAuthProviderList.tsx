import type { Component } from 'solid-js'
import type { OAuthProviderInfo } from '~/generated/leapmux/v1/auth_pb'

import { For } from 'solid-js'
import * as styles from './LoginPage.css'

interface OAuthProviderListProps {
  providers: OAuthProviderInfo[]
  verb: string
  dividerText: string
  buildUrl?: (provider: OAuthProviderInfo) => string
}

export const OAuthProviderList: Component<OAuthProviderListProps> = (props) => {
  const url = (provider: OAuthProviderInfo) =>
    props.buildUrl ? props.buildUrl(provider) : provider.loginUrl

  return (
    <>
      <div class="vstack gap-2">
        <For each={props.providers}>
          {provider => (
            <a href={url(provider)} class={styles.oauthButton}>
              {props.verb}
              {' '}
              {provider.name}
            </a>
          )}
        </For>
      </div>
      <div class={styles.divider}>
        <span>{props.dividerText}</span>
      </div>
    </>
  )
}
