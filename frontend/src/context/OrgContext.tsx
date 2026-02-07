import type { ParentComponent } from 'solid-js'
import type { Org } from '~/generated/leapmux/v1/org_pb'
import { useParams } from '@solidjs/router'
import { createContext, createEffect, createMemo, createSignal, useContext } from 'solid-js'
import { orgClient } from '~/api/clients'

interface OrgState {
  slug: () => string
  orgId: () => string
  org: () => Org | null
  orgs: () => Org[]
  loading: () => boolean
  notFound: () => boolean
  refetch: () => Promise<void>
}

const OrgContext = createContext<OrgState>()

export const OrgProvider: ParentComponent = (props) => {
  const params = useParams<{ orgSlug: string }>()
  const [orgs, setOrgs] = createSignal<Org[]>([])
  const [loading, setLoading] = createSignal(true)
  const [fetchError, setFetchError] = createSignal(false)

  const fetchOrgs = async () => {
    setLoading(true)
    setFetchError(false)
    try {
      const resp = await orgClient.listMyOrgs({})
      setOrgs(resp.orgs)
    }
    catch {
      setFetchError(true)
    }
    finally {
      setLoading(false)
    }
  }

  // Fetch orgs on mount and when slug changes
  createEffect(() => {
    void params.orgSlug
    fetchOrgs()
  })

  const currentOrg = () => orgs().find(o => o.name === params.orgSlug) ?? null
  const orgId = () => currentOrg()?.id ?? ''

  // Not found: loading finished without error but no org matches the slug.
  const notFound = createMemo(() => !loading() && !fetchError() && currentOrg() === null)

  const state: OrgState = {
    slug: () => params.orgSlug,
    orgId,
    org: currentOrg,
    orgs,
    loading,
    notFound,
    refetch: fetchOrgs,
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
