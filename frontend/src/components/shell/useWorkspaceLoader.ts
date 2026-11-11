import type { Section, SectionItem, Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { createSectionStore } from '~/stores/section.store'
import type { createWorkspaceStore } from '~/stores/workspace.store'
import { equals } from '@bufbuild/protobuf'
import { createComputed, createEffect, on, onCleanup, untrack } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import { showWarnToast, showWarnToastUnlessDisconnected } from '~/components/common/Toast'
import { WorkspaceSchema } from '~/generated/proto/leapmux/v1/workspace_pb'
import { formatErrorMessage } from '~/lib/errors'
import { createIdentityCache } from '~/lib/identityCache'

interface UseWorkspaceLoaderOpts {
  userId: () => string | undefined
  workspaceStore: ReturnType<typeof createWorkspaceStore>
  sectionStore: ReturnType<typeof createSectionStore>
}

export function useWorkspaceLoader(opts: UseWorkspaceLoaderOpts) {
  const { workspaceStore, sectionStore } = opts

  // Stabilize the identity of fetched proto objects so the sidebar's
  // <For>s don't unmount and remount every row when the lists are
  // refreshed (e.g. after a moveSection rollback). See `lib/identityCache.ts`.
  const workspaceIdentity = createIdentityCache<Workspace>({
    keyOf: w => w.id,
    // By value: `createdAt` is a nested Timestamp message that every reply
    // decodes afresh, so the default shallow comparison never matched.
    equals: (cached, fresh) => equals(WorkspaceSchema, cached, fresh),
  })
  const sectionIdentity = createIdentityCache<Section>({
    keyOf: s => s.id,
  })
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

  let generation = 0
  let disposed = false
  createComputed(() => {
    opts.userId()
    generation++
    untrack(() => {
      workspaceStore.reset()
      sectionStore.reset()
    })
    workspaceIdentity.clear()
    sectionIdentity.clear()
    sectionItemIdentity.clear()
  })
  onCleanup(() => {
    disposed = true
  })
  function captureLifetime() {
    const account = untrack(opts.userId)
    const captured = generation
    return () => !disposed && Boolean(account) && opts.userId() === account && captured === generation
  }

  const loadWorkspaces = async () => {
    const isCurrent = captureLifetime()
    if (!isCurrent())
      return
    const seq = ++workspaceSeq
    workspaceStore.setLoading(true)
    try {
      const resp = await workspaceClient.listWorkspaces({})
      if (!isCurrent() || seq !== workspaceSeq)
        return
      workspaceStore.setError(null)
      workspaceStore.setWorkspaces(workspaceIdentity.stabilize(resp.workspaces as Workspace[]))
    }
    catch (err) {
      if (!isCurrent() || seq !== workspaceSeq)
        return
      // The recorded error is what keeps the shell from concluding the active
      // workspace is gone and switching away from it (see
      // `resolveActiveWorkspace`); the toast is what tells the user their
      // sidebar is stale rather than empty.
      workspaceStore.setError(formatErrorMessage(err, 'Failed to load workspaces'))
      showWarnToastUnlessDisconnected('Failed to load workspaces', err)
    }
    finally {
      if (isCurrent() && seq === workspaceSeq) {
        workspaceStore.setLoading(false)
        // Marked on BOTH outcomes: `loaded` means "an attempt finished", which
        // is what lets resolveActiveWorkspace tell "you own nothing" apart from
        // "we have not asked yet". The error path is separately recorded above.
        workspaceStore.markLoaded()
      }
    }
  }

  const loadSections = async () => {
    const isCurrent = captureLifetime()
    if (!isCurrent())
      return
    const seq = ++sectionSeq
    sectionStore.setLoading(true)
    try {
      const resp = await sectionClient.listSections({})
      if (!isCurrent() || seq !== sectionSeq)
        return
      sectionStore.setError(null)
      sectionStore.setSections(sectionIdentity.stabilize(resp.sections))
      sectionStore.setItems(sectionItemIdentity.stabilize(resp.items))
    }
    catch (err) {
      if (!isCurrent() || seq !== sectionSeq)
        return
      sectionStore.setError(formatErrorMessage(err, 'Failed to load sections'))
      showWarnToastUnlessDisconnected('Failed to load sections', err)
    }
    finally {
      if (isCurrent() && seq === sectionSeq) {
        sectionStore.setLoading(false)
        // Marked on BOTH outcomes: AppShell's boot overlay waits on `loaded`
        // the same way it waits on the workspace list — "an attempt finished".
        sectionStore.markLoaded()
      }
    }
  }

  createEffect(on(opts.userId, (account) => {
    if (!account)
      return
    void loadWorkspaces()
    void loadSections()
  }))

  const handleMoveSection = (sectionId: string, sidebar: Sidebar, position: string) => {
    sectionStore.moveSection(sectionId, sidebar, position)
  }

  const handleMoveSectionServer = (sectionId: string, sidebar: Sidebar, position: string) => {
    const isCurrent = captureLifetime()
    if (!isCurrent())
      return
    sectionClient.moveSection({ sectionId, sidebar, position })
      .catch((err) => {
        if (!isCurrent())
          return
        showWarnToast('Failed to move section', err)
        loadSections()
      })
  }

  return {
    loadWorkspaces,
    loadSections,
    handleMoveSection,
    handleMoveSectionServer,
  }
}
