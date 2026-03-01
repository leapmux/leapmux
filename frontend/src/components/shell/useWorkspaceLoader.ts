import type { Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { createSectionStore } from '~/stores/section.store'
import type { createWorkspaceStore } from '~/stores/workspace.store'
import { createEffect } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import { showToast } from '~/components/common/Toast'

interface UseWorkspaceLoaderOpts {
  getOrgId: () => string | undefined
  workspaceStore: ReturnType<typeof createWorkspaceStore>
  sectionStore: ReturnType<typeof createSectionStore>
}

export function useWorkspaceLoader(opts: UseWorkspaceLoaderOpts) {
  const { getOrgId, workspaceStore, sectionStore } = opts

  const loadWorkspaces = async () => {
    const orgId = getOrgId()
    if (!orgId)
      return
    workspaceStore.setLoading(true)
    try {
      const resp = await workspaceClient.listWorkspaces({ orgId })
      workspaceStore.setWorkspaces(resp.workspaces)
    }
    catch (err) {
      workspaceStore.setError(String(err))
    }
    finally {
      workspaceStore.setLoading(false)
    }
  }

  createEffect(() => {
    if (getOrgId()) {
      loadWorkspaces()
    }
  })

  const loadSections = async () => {
    const orgId = getOrgId()
    if (!orgId)
      return
    sectionStore.setLoading(true)
    try {
      const resp = await sectionClient.listSections({ orgId })
      sectionStore.setSections(resp.sections)
      sectionStore.setItems(resp.items)
    }
    catch (err) {
      sectionStore.setError(err instanceof Error ? err.message : 'Failed to load sections')
    }
    finally {
      sectionStore.setLoading(false)
    }
  }

  createEffect(() => {
    if (getOrgId()) {
      loadSections()
    }
  })

  const handleMoveSection = (sectionId: string, sidebar: Sidebar, position: string) => {
    sectionStore.moveSection(sectionId, sidebar, position)
  }

  const handleMoveSectionServer = (sectionId: string, sidebar: Sidebar, position: string) => {
    sectionClient.moveSection({ sectionId, sidebar, position })
      .catch((err) => {
        showToast(err instanceof Error ? err.message : 'Failed to move section', 'danger')
        loadSections()
      })
  }

  return { loadWorkspaces, loadSections, handleMoveSection, handleMoveSectionServer }
}
