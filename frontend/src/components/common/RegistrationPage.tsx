import type { Component } from 'solid-js'
import type { Org } from '~/generated/leapmux/v1/org_pb'
import type { GetRegistrationResponse } from '~/generated/leapmux/v1/worker_pb'
import { useNavigate } from '@solidjs/router'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, For, onMount, Show } from 'solid-js'
import { orgClient, workerClient } from '~/api/clients'
import { Icon } from '~/components/common/Icon'
import { RegistrationStatus } from '~/generated/leapmux/v1/worker_pb'
import { spinner } from '~/styles/animations.css'
import { NotFoundPage } from './NotFoundPage'
import * as styles from './RegistrationPage.css'

interface RegistrationPageProps {
  token: string
}

export const RegistrationPage: Component<RegistrationPageProps> = (props) => {
  const navigate = useNavigate()
  const [registration, setRegistration] = createSignal<GetRegistrationResponse | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [notFound, setNotFound] = createSignal(false)
  const [orgs, setOrgs] = createSignal<Org[]>([])
  const [selectedOrgId, setSelectedOrgId] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [approved, setApproved] = createSignal(false)
  const [approvedWorkerId, setApprovedWorkerId] = createSignal<string | null>(null)

  onMount(async () => {
    try {
      const [regResp, orgResp] = await Promise.all([
        workerClient.getRegistration({ registrationToken: props.token }),
        orgClient.listMyOrgs({}),
      ])
      setRegistration(regResp)
      setOrgs(orgResp.orgs)
      if (orgResp.orgs.length > 0) {
        const personal = orgResp.orgs.find(o => o.isPersonal)
        setSelectedOrgId(personal?.id ?? orgResp.orgs[0].id)
      }
    }
    catch (e) {
      const msg = e instanceof Error ? e.message : 'Failed to load registration'
      if (msg.toLowerCase().includes('not found')) {
        setNotFound(true)
      }
      else {
        setError(msg)
      }
    }
    finally {
      setLoading(false)
    }
  })

  const handleApprove = async (e: Event) => {
    e.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      const resp = await workerClient.approveRegistration({
        registrationToken: props.token,
        orgId: selectedOrgId(),
      })
      setApproved(true)
      if (resp.workerId) {
        setApprovedWorkerId(resp.workerId)
      }
    }
    catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to approve registration')
    }
    finally {
      setSubmitting(false)
    }
  }

  // Determine the dashboard link based on selected org
  const dashboardLink = () => {
    const org = orgs().find(o => o.id === selectedOrgId())
    return org ? `/o/${org.name}` : '/'
  }

  return (
    <Show
      when={!loading()}
      fallback={(
        <div class={styles.container}>
          <div class={`card ${styles.authCardXWide}`}>
            <h1>Approve Worker</h1>
            <div class={styles.successText}>Loading registration...</div>
          </div>
        </div>
      )}
    >
      <Show
        when={!notFound()}
        fallback={(
          <NotFoundPage
            title="Registration Not Found"
            message="This registration link is invalid or has expired."
            linkHref="/login"
            linkText="Go to login"
          />
        )}
      >
        <Show when={registration()?.status === RegistrationStatus.APPROVED}>
          <NotFoundPage
            title="Already Registered"
            message="This worker has already been registered."
            linkHref={dashboardLink()}
            linkText="Go to dashboard"
          />
        </Show>
        <Show when={registration()?.status === RegistrationStatus.EXPIRED}>
          <NotFoundPage
            title="Registration Expired"
            message="This registration has expired."
            linkHref={dashboardLink()}
            linkText="Go to dashboard"
          />
        </Show>
        <Show when={registration()?.status !== RegistrationStatus.APPROVED && registration()?.status !== RegistrationStatus.EXPIRED}>
          <div class={styles.container}>
            <div class={`card ${styles.authCardXWide}`}>
              <Show
                when={approved()}
                fallback={(
                  <>
                    <h1>Approve Worker</h1>
                    <div class={styles.warningBox}>
                      Only approve workers that you trust. A registered worker will have access to your workspace data.
                    </div>
                    <div class={styles.infoGrid}>
                      <Show when={registration()!.version}>
                        <span class={styles.infoLabel}>Version</span>
                        <span class={styles.infoValue}>{registration()!.version}</span>
                      </Show>
                      <Show when={registration()!.publicKeyFingerprint}>
                        <span class={styles.infoLabel}>Key Fingerprint</span>
                        <span class={styles.infoValue}><code>{registration()!.publicKeyFingerprint}</code></span>
                      </Show>
                    </div>

                    <form class="vstack gap-4" onSubmit={handleApprove}>
                      <label>
                        Organization
                        <select
                          value={selectedOrgId()}
                          onChange={e => setSelectedOrgId(e.currentTarget.value)}
                        >
                          <For each={orgs()}>
                            {o => (
                              <option value={o.id}>
                                {o.name}
                                {o.isPersonal ? ' (personal)' : ''}
                              </option>
                            )}
                          </For>
                        </select>
                      </label>
                      <Show when={error()}>
                        <div class={styles.errorText}>{error()}</div>
                      </Show>
                      <button
                        type="submit"
                        disabled={submitting() || !selectedOrgId()}
                      >
                        <Show when={submitting()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
                        {submitting() ? 'Approving...' : 'Approve'}
                      </button>
                    </form>
                  </>
                )}
              >
                <h1>Worker Registered Successfully</h1>
                <p class={styles.successText}>
                  Your worker has been registered and is ready to use.
                  You can now create a workspace to start working.
                </p>
                <div class={styles.actionRow}>
                  <a
                    class={styles.link}
                    href={`${dashboardLink()}?newWorkspace=true${approvedWorkerId() ? `&workerId=${approvedWorkerId()}` : ''}`}
                    onClick={(e) => {
                      e.preventDefault()
                      const url = `${dashboardLink()}?newWorkspace=true${approvedWorkerId() ? `&workerId=${approvedWorkerId()}` : ''}`
                      navigate(url)
                    }}
                  >
                    Create Workspace
                  </a>
                  <a
                    class={styles.linkSecondary}
                    href={dashboardLink()}
                    onClick={(e) => {
                      e.preventDefault()
                      navigate(dashboardLink())
                    }}
                  >
                    Go to Dashboard
                  </a>
                </div>
              </Show>
            </div>
          </div>
        </Show>
      </Show>
    </Show>
  )
}
