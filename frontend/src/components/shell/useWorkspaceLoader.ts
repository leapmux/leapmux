import type { Section, SectionItem, Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createSectionStore } from '~/stores/section.store'
import type { createWorkspaceStore } from '~/stores/workspace.store'
import { onMount } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { formatErrorMessage } from '~/lib/errors'
import { createIdentityCache } from '~/lib/identityCache'

interface UseWorkspaceLoaderOpts {
  workspaceStore: ReturnType<typeof createWorkspaceStore>
  sectionStore: ReturnType<typeof createSectionStore>
}

export function useWorkspaceLoader(opts: UseWorkspaceLoaderOpts) {
  const { workspaceStore, sectionStore } = opts

  // Stabilize the identity of fetched proto objects so the sidebar's
  // <For>s don't unmount and remount every row when the lists are
  // refreshed (e.g. after a moveSection rollback). See `lib/identityCache.ts`.
  const workspaceIdentity = createIdentityCache<Workspace>({ keyOf: w => w.id })
  const sectionIdentity = createIdentityCache<Section>({ keyOf: s => s.id })
  const sectionItemIdentity = createIdentityCache<SectionItem>({
    // SectionItems don't have their own id; (sectionId, workspaceId) is unique.
    keyOf: i => `${i.sectionId}\u0000${i.workspaceId}`,
  })

  // Both loaders are fired from several places at once -- mount, the workspace
  // lifecycle stream, the create/delete dialogs, useWorkspaceOperations -- so
  // two requests are routinely in flight together. Without a sequence number
  // whichever ANSWERS last wins regardless of which was ASKED last, so a slow
  // earlier response resurrects a just-deleted workspace (or drops a
  // just-created one) until the next lifecycle event, and the first response
  // to land clears `loading` while another request is still outstanding. Each
  // load stamps itself and only writes back if it is still the newest.
  let workspaceSeq = 0
  let sectionSeq = 0

  const loadWorkspaces = async () => {
    const seq = ++workspaceSeq
    workspaceStore.setLoading(true)
    try {
      const resp = await workspaceClient.listWorkspaces({})
      if (seq !== workspaceSeq)
        return
      workspaceStore.setError(null)
      workspaceStore.setWorkspaces(workspaceIdentity.stabilize(resp.workspaces as Workspace[]))
    }
    catch (err) {
      if (seq !== workspaceSeq)
        return
      // The recorded error is what keeps the shell from concluding the active
      // workspace is gone and switching away from it (see
      // `resolveActiveWorkspace`); the toast is what tells the user their
      // sidebar is stale rather than empty.
      workspaceStore.setError(formatErrorMessage(err, 'Failed to load workspaces'))
      showWarnToast('Failed to load workspaces', err)
    }
    finally {
      if (seq === workspaceSeq) {
        workspaceStore.setLoading(false)
        // Marked on BOTH outcomes: `loaded` means "an attempt finished", which
        // is what lets resolveActiveWorkspace tell "you own nothing" apart from
        // "we have not asked yet". The error path is separately recorded above.
        workspaceStore.markLoaded()
      }
    }
  }

  // Seed the sidebar once. The hook is only instantiated inside the
  // authenticated shell (AuthGuard renders AppShell only after the
  // session is restored), so there is no "wait for the user" gate to
  // re-run on -- later refreshes come from workspace-lifecycle events.
  onMount(() => {
    void loadWorkspaces()
  })

  const loadSections = async () => {
    const seq = ++sectionSeq
    sectionStore.setLoading(true)
    try {
      const resp = await sectionClient.listSections({})
      if (seq !== sectionSeq)
        return
      sectionStore.setError(null)
      sectionStore.setSections(sectionIdentity.stabilize(resp.sections))
      sectionStore.setItems(sectionItemIdentity.stabilize(resp.items))
    }
    catch (err) {
      if (seq !== sectionSeq)
        return
      sectionStore.setError(formatErrorMessage(err, 'Failed to load sections'))
      showWarnToast('Failed to load sections', err)
    }
    finally {
      if (seq === sectionSeq)
        sectionStore.setLoading(false)
    }
  }

  onMount(() => {
    void loadSections()
  })

  const handleMoveSection = (sectionId: string, sidebar: Sidebar, position: string) => {
    sectionStore.moveSection(sectionId, sidebar, position)
  }

  const handleMoveSectionServer = (sectionId: string, sidebar: Sidebar, position: string) => {
    sectionClient.moveSection({ sectionId, sidebar, position })
      .catch((err) => {
        showWarnToast('Failed to move section', err)
        loadSections()
      })
  }

  return { loadWorkspaces, loadSections, handleMoveSection, handleMoveSectionServer }
}
