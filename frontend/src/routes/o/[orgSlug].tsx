import type { RouteSectionProps } from '@solidjs/router'
import type { ParentComponent } from 'solid-js'
import { Show } from 'solid-js'
import { AuthGuard } from '~/components/common/AuthGuard'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { AppShell } from '~/components/shell/AppShell'
import { OrgProvider, useOrg } from '~/context/OrgContext'
import { WorkspaceProvider } from '~/context/WorkspaceContext'

const OrgContent: ParentComponent = (props) => {
  const org = useOrg()

  return (
    <Show
      when={!org.notFound()}
      fallback={<NotFoundPage linkHref="/" linkText="Go to Dashboard" />}
    >
      <WorkspaceProvider>
        <AppShell>{props.children}</AppShell>
      </WorkspaceProvider>
    </Show>
  )
}

export default function OrgLayout(props: RouteSectionProps) {
  return (
    <AuthGuard>
      <OrgProvider>
        <OrgContent>{props.children}</OrgContent>
      </OrgProvider>
    </AuthGuard>
  )
}
