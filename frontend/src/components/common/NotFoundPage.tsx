import type { Component } from 'solid-js'
import { A } from '@solidjs/router'
import { authCardWide } from '~/styles/shared.css'
import * as styles from './NotFoundPage.css'

interface NotFoundPageProps {
  title?: string
  message?: string
  linkHref: string
  linkText: string
}

export const NotFoundPage: Component<NotFoundPageProps> = (props) => {
  return (
    <div class={styles.container}>
      <div class={`card ${authCardWide}`}>
        <h1>{props.title ?? 'Not Found'}</h1>
        <p class={styles.message}>{props.message ?? 'The page you\'re looking for doesn\'t exist.'}</p>
        <A class={styles.link} href={props.linkHref}>{props.linkText}</A>
      </div>
    </div>
  )
}
