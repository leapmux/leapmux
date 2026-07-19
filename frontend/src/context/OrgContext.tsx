import type { ParentComponent } from 'solid-js'
import { useParams } from '@solidjs/router'
import { createContext, createMemo, useContext } from 'solid-js'
import { useAuth } from '~/context/AuthContext'

interface OrgState {
  slug: () => string
  orgId: () => string
  notFound: () => boolean
}

const OrgContext = createContext<OrgState>()

/**
 * Thin adapter exposing the user's personal org (one per user, name ==
 * username) under the `/o/{orgSlug}` route. The org identity comes from
 * the authenticated user (`orgId`/`orgName` on the auth User); the slug
 * comes from the route param.
 */
export const OrgProvider: ParentComponent = (props) => {
  const params = useParams<{ orgSlug: string }>()
  const auth = useAuth()

  // Not found: the user is loaded and the route slug isn't their org. The
  // comparison is case-insensitive because orgName mirrors the store-normalized
  // (lowercased) username while orgSlug is the verbatim URL param -- without the
  // fold, a user hand-typing or bookmarking their own org URL with any capital
  // (`/o/Alice` for org `alice`) would be shown a not-found page for their own org.
  const notFound = createMemo(() => {
    const user = auth.user()
    return user !== null && params.orgSlug.toLowerCase() !== user.orgName
  })

  const state: OrgState = {
    slug: () => params.orgSlug,
    orgId: () => auth.user()?.orgId ?? '',
    notFound,
  }

  return (
    <OrgContext.Provider value={state}>
      {props.children}
    </OrgContext.Provider>
  )
}

export function useOrg(): OrgState {
  const ctx = useContext(OrgContext)
  if (!ctx) {
    throw new Error('useOrg must be used within OrgProvider')
  }
  return ctx
}
