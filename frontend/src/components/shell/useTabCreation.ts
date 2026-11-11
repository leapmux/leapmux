import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Accessor } from 'solid-js'
import type { AgentInfo, OpenAgentRequest } from '~/generated/proto/leapmux/v1/agent_pb'
import type { PublishedTab, TabCreation, TabPlacement, TabPresentation } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { FileOpenSource, Tab } from '~/stores/tab.types'
import type { TabMetadataStore, TabWritesLanded } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { clone, create, toJsonString } from '@bufbuild/protobuf'
import { Code } from '@connectrpc/connect'
import { batch, createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import { workerAgent, workerPrivate, workerTerminal } from '~/api/workerRpc'
import { OpenAgentRequestSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { TabCreationSchema, TabInsertion, TabPlacementSchema, TabPresentationSchema } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import { OpenTerminalRequestSchema } from '~/generated/proto/leapmux/v1/terminal_pb'
import { RegisterTabPayloadRequestSchema } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { resolveSettingsTabFields } from '~/hooks/agentEvents'
import { onStorageAccountChange } from '~/lib/browserStorage'
import { ChannelError } from '~/lib/channelError'
import { cmpStr, hlcIsZero } from '~/lib/crdt'
import { randomUUID } from '~/lib/idGenerator'
import { basename } from '~/lib/paths'
import { tabPayloadView } from '~/lib/tabPayload'
import { parseTabKey, planOptimisticRepoGit, protoToAgentTabFields, tabKey, withoutLiveStatusFields } from '~/stores/tab.helpers'
import { tabPayloadMetadata, watchTabWrites } from '~/stores/tabMetadata.store'

export interface TabCreationOrigin {
  workspaceId: string
  workerId: string
  /** Distinguishes independent UI actions with the same resource configuration. */
  retryKey: string
  /** The current instance of the initiating dialog, when a dialog owns the action. */
  viewIdentity?: Accessor<unknown>
  /** Account-scoped bookkeeping after the Worker confirms publication. */
  onCommitted?: (publication: PublishedTab, isCurrent: () => boolean) => void
  /** Local UI completion after the canonical tab arrives. */
  onObserved?: () => void
  onError: (error: unknown) => void
}

export interface AgentTabCreation extends TabCreationOrigin {
  request: Omit<MessageInitShape<typeof OpenAgentRequestSchema>, 'creation' | '$typeName'>
  seedGitFromActiveTab?: boolean
}

export interface TerminalTabCreation extends TabCreationOrigin {
  request: Omit<MessageInitShape<typeof OpenTerminalRequestSchema>, 'target' | '$typeName'>
  seedGitFromActiveTab?: boolean
}

export interface PayloadTabCreation extends TabCreationOrigin {
  request: Omit<MessageInitShape<typeof RegisterTabPayloadRequestSchema>, 'creation' | '$typeName'>
  fileOpenSource?: FileOpenSource
  presentation?: MessageInitShape<typeof TabPresentationSchema>
}

export interface WorkspaceTabCreation extends Omit<AgentTabCreation, 'workspaceId'> {
  title: string
}

export type WorkspaceCreationSubmission = Omit<WorkspaceTabCreation, 'retryKey' | 'viewIdentity' | 'onObserved'>

export type AgentCreationSubmission = Omit<AgentTabCreation, 'workspaceId' | 'retryKey' | 'viewIdentity' | 'onObserved'>
export type TerminalCreationSubmission = Omit<TerminalTabCreation, 'workspaceId' | 'retryKey' | 'viewIdentity' | 'onObserved'>

export interface UseTabCreationOptions {
  userId: Accessor<string | undefined>
  state: Accessor<UserCrdtState | null>
  activeWorkspaceId: Accessor<string | null | undefined>
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  repoGitStore: ReturnType<typeof createRepoGitStore>
  settingsPendingAxes: (id: string) => ReadonlySet<string>
  selectWorkspace: (id: string) => void
  focusEditor: () => void
}

interface CreationFrame {
  account: string
  generation: number
  activeWorkspace: string | null | undefined
  focusedTile: string | null
  sourceTab: Tab | undefined
  selectedWorkspaceTab: string | null | undefined
  selectedFocusedTab: string | null | undefined
  view: Accessor<unknown> | undefined
  viewIdentity: unknown
}

interface CreationObserver {
  workspaceId: string
  onObserved: TabCreationOrigin['onObserved']
  onError: TabCreationOrigin['onError']
  frame: CreationFrame
  destinationFocus: string | null
  selectedDestinationTab: string | null | undefined
  selectedPlacementTab: string | null | undefined
  resolve: (observed: boolean) => void
}

interface CreationAttempt {
  key: string
  frame: CreationFrame
  creation: TabCreation
  type: TabType
  workerId: string
  controller: AbortController
  sending: boolean
  seenLive: boolean
  publication: PublishedTab | undefined
  send: () => Promise<PublishedTab>
  onCommitted: TabCreationOrigin['onCommitted']
  observers: Set<CreationObserver>
}

type CreationContext = Omit<CreationAttempt, 'send'>

interface WorkspaceAttempt {
  controller: AbortController
  result: Promise<string>
  attempt?: CreationAttempt
}

interface AttemptPreparation {
  origin: TabCreationOrigin
  frame: CreationFrame
  type: TabType
  requestKey: string
  presentation?: TabPresentation | undefined
  placement: () => TabPlacement
  prepare: (attempt: CreationContext) => CreationAttempt['send']
}

function captureAgentRequest(input: AgentTabCreation['request']): OpenAgentRequest {
  const request = clone(OpenAgentRequestSchema, create(OpenAgentRequestSchema, input))
  request.creation = undefined
  request.options = Object.fromEntries(Object.entries(request.options).sort(([left], [right]) => cmpStr(left, right)))
  return request
}

export function isDefinitiveTabCreationError(error: unknown): error is ChannelError {
  return error instanceof ChannelError && error.source === 'rpc' && error.code === Code.Aborted
}

function runLocalCallback(callback: () => void) {
  try {
    callback()
  }
  catch (error) {
    console.warn('Tab creation callback failed', error)
  }
}

/** Owns immutable attempts and local completion while canonical state owns tabs. */
export function useTabCreation(options: UseTabCreationOptions) {
  const attempts = new Map<string, CreationAttempt>()
  const workspaces = new Map<string, WorkspaceAttempt>()
  const [revision, setRevision] = createSignal(0)
  let account = untrack(options.userId)
  let generation = 0
  let disposed = false
  const changed = () => setRevision(value => value + 1)

  function accountIsCurrent(frame: CreationFrame) {
    return !disposed && frame.account === options.userId() && frame.generation === generation
  }

  function viewIsCurrent(frame: CreationFrame) {
    return !frame.view || frame.view() === frame.viewIdentity
  }

  function forgetAttempts() {
    generation++
    for (const attempt of attempts.values()) {
      attempt.controller.abort()
      for (const observer of attempt.observers)
        observer.resolve(false)
    }
    for (const workspace of workspaces.values())
      workspace.controller.abort()
    attempts.clear()
    workspaces.clear()
  }

  function syncAccount() {
    const current = options.userId()
    if (current !== account) {
      account = current
      forgetAttempts()
    }
  }

  function captureFrame(origin: Pick<TabCreationOrigin, 'viewIdentity'>): CreationFrame | undefined {
    syncAccount()
    if (disposed || !account)
      return undefined
    const activeWorkspace = options.activeWorkspaceId()
    const focusedTile = activeWorkspace
      ? options.layoutStore.focusedTileIdFor(activeWorkspace)
      : null
    const source = focusedTile
      ? options.selection.activeTabForTile(focusedTile)
      : activeWorkspace
        ? options.selection.activeTabForWorkspace(activeWorkspace)
        : undefined
    return {
      account,
      generation,
      activeWorkspace,
      focusedTile,
      // Retain Git facts without retaining a terminal screen or agent catalogs.
      sourceTab: source
        ? {
            id: source.id,
            workspaceId: source.workspaceId,
            type: source.type,
            workerId: source.workerId,
            workingDir: source.workingDir,
            gitToplevel: source.gitToplevel,
            ...(source.type === TabType.TERMINAL
              ? { shellStartDir: source.shellStartDir }
              : {}),
          }
        : undefined,
      selectedWorkspaceTab: activeWorkspace ? options.selection.state.activeByWorkspace[activeWorkspace] : undefined,
      selectedFocusedTab: focusedTile ? options.selection.state.activeByTile[focusedTile] : undefined,
      view: origin.viewIdentity,
      viewIdentity: origin.viewIdentity?.(),
    }
  }

  function capturePlacement(workspaceId: string): TabPlacement {
    const currentAccount = options.userId()
    if (!currentAccount || options.state()?.userId !== currentAccount)
      throw new Error('The workspace is not ready for a new tab yet.')
    const tileId = workspaceId === options.activeWorkspaceId()
      ? options.layoutStore.placementTileId()
      : options.layoutStore.focusedLeafIdFor(workspaceId) ?? options.layoutStore.firstLeafIdFor(workspaceId)
    if (!tileId)
      throw new Error('The workspace is not ready for a new tab yet.')
    const selected = options.selection.activeKeyForTile(tileId)
    const anchor = selected
      ? parseTabKey(selected)
      : null
    return create(TabPlacementSchema, {
      workspaceId,
      destination: { case: 'tileId', value: tileId },
      insertion: anchor ? TabInsertion.AFTER : TabInsertion.LAST,
      relativeTabId: anchor?.id ?? '',
    })
  }

  function attemptEnded(attempt: CreationContext) {
    const state = options.state()
    if (state?.userId !== attempt.frame.account)
      return false
    const record = state.tabs[attempt.creation.tabId]
    if (!record)
      return attempt.seenLive
    if (!hlcIsZero(record.tombstoneAt))
      return true
    if (record.tabType !== attempt.type)
      return true
    if (record.workerId && record.workerId.value !== attempt.workerId)
      return true
    attempt.seenLive = true
    return false
  }

  function validatePublication(attempt: CreationContext, publication: PublishedTab | undefined): PublishedTab {
    if (!publication || publication.tab?.tabId !== attempt.creation.tabId
      || publication.tab.tabType !== attempt.type
      || publication.workspaceId !== attempt.creation.placement?.workspaceId
      || !publication.tileId || !publication.position) {
      throw new Error('The Worker returned an invalid tab publication.')
    }
    return publication
  }

  function selectionIsCurrent(observer: CreationObserver, attempt: CreationAttempt) {
    const frame = observer.frame
    const workspaceId = observer.workspaceId
    const tileId = attempt.creation.placement?.destination.case === 'tileId'
      ? attempt.creation.placement.destination.value
      : undefined
    return options.activeWorkspaceId() === frame.activeWorkspace
      && (!frame.activeWorkspace || options.layoutStore.focusedTileIdFor(frame.activeWorkspace) === frame.focusedTile)
      && (!frame.activeWorkspace || options.selection.state.activeByWorkspace[frame.activeWorkspace] === frame.selectedWorkspaceTab)
      && (!frame.focusedTile || options.selection.state.activeByTile[frame.focusedTile] === frame.selectedFocusedTab)
      && options.layoutStore.focusedTileIdFor(workspaceId) === observer.destinationFocus
      && options.selection.state.activeByWorkspace[workspaceId] === observer.selectedDestinationTab
      && (!tileId || options.selection.state.activeByTile[tileId] === observer.selectedPlacementTab)
  }

  function focusAfterCompletion(observer: CreationObserver, tab: Tab) {
    queueMicrotask(() => {
      if (!accountIsCurrent(observer.frame))
        return
      const completedView = observer.frame.view?.()
      if (observer.frame.view && completedView !== observer.frame.viewIdentity && completedView !== null)
        return
      requestAnimationFrame(() => {
        if (!accountIsCurrent(observer.frame) || observer.frame.view?.() !== completedView
          || options.activeWorkspaceId() !== tab.workspaceId
          || options.selection.state.activeByWorkspace[tab.workspaceId] !== tabKey(tab)
          || options.layoutStore.focusedTileIdFor(tab.workspaceId) !== tab.tileId) {
          return
        }
        options.focusEditor()
      })
    })
  }

  function retireAttempt(attempt: CreationAttempt) {
    if (attempts.get(attempt.key) !== attempt)
      return
    attempts.delete(attempt.key)
    for (const [key, workspace] of workspaces) {
      if (workspace.attempt === attempt)
        workspaces.delete(key)
    }
  }

  const stopStorageObservation = onStorageAccountChange(() => {
    forgetAttempts()
    changed()
  })

  createEffect(() => {
    options.userId()
    untrack(syncAccount)
    revision()
    options.state()
    for (const attempt of attempts.values()) {
      const ended = attemptEnded(attempt)
      if (ended)
        retireAttempt(attempt)
      for (const observer of attempt.observers) {
        if (!accountIsCurrent(observer.frame) || !viewIsCurrent(observer.frame) || ended) {
          attempt.observers.delete(observer)
          observer.resolve(false)
          continue
        }
        if (!attempt.publication || options.state()?.userId !== observer.frame.account)
          continue
        const tab = options.view.getById(attempt.type, attempt.creation.tabId)
        if (!tab || tab.workerId !== attempt.workerId)
          continue
        const activate = selectionIsCurrent(observer, attempt)
        attempt.observers.delete(observer)
        batch(() => {
          if (activate) {
            if (options.activeWorkspaceId() !== tab.workspaceId)
              options.selectWorkspace(tab.workspaceId)
            options.selection.setActive(tab)
            if (tab.tileId)
              options.layoutStore.setFocusedTile(tab.tileId, tab.workspaceId)
          }
          runLocalCallback(() => observer.onObserved?.())
          observer.resolve(true)
          if (activate)
            focusAfterCompletion(observer, tab)
        })
      }
      if (attempt.publication && attempt.observers.size === 0
        && options.state()?.userId === attempt.frame.account
        && options.view.getById(attempt.type, attempt.creation.tabId)) {
        retireAttempt(attempt)
      }
    }
  })

  onCleanup(() => {
    disposed = true
    stopStorageObservation()
    forgetAttempts()
  })

  function sendAttempt(attempt: CreationAttempt) {
    if (attempt.sending || attempt.publication)
      return
    attempt.sending = true
    void Promise.resolve().then(attempt.send).then((publication) => {
      attempt.sending = false
      if (!accountIsCurrent(attempt.frame) || attempts.get(attempt.key) !== attempt || attemptEnded(attempt))
        return
      attempt.publication = publication
      runLocalCallback(() => attempt.onCommitted?.(publication, () => accountIsCurrent(attempt.frame)))
      changed()
    }, (error: unknown) => {
      attempt.sending = false
      if (!accountIsCurrent(attempt.frame) || attempts.get(attempt.key) !== attempt)
        return
      if (isDefinitiveTabCreationError(error)) {
        attempts.delete(attempt.key)
        if (!options.state()?.tabs[attempt.creation.tabId])
          options.metadata.remove(attempt.creation.tabId)
      }
      for (const observer of attempt.observers) {
        if (accountIsCurrent(observer.frame) && viewIsCurrent(observer.frame))
          runLocalCallback(() => observer.onError(error))
        observer.resolve(false)
      }
      attempt.observers.clear()
      changed()
    })
  }

  function observe(attempt: CreationAttempt, origin: TabCreationOrigin, frame: CreationFrame): Promise<boolean> {
    const tileId = attempt.creation.placement?.destination.case === 'tileId'
      ? attempt.creation.placement.destination.value
      : undefined
    return new Promise((resolve) => {
      attempt.observers.add({
        workspaceId: origin.workspaceId,
        onObserved: origin.onObserved,
        onError: origin.onError,
        frame,
        resolve,
        destinationFocus: options.layoutStore.focusedTileIdFor(origin.workspaceId),
        selectedDestinationTab: options.selection.state.activeByWorkspace[origin.workspaceId],
        selectedPlacementTab: tileId ? options.selection.state.activeByTile[tileId] : undefined,
      })
      changed()
      sendAttempt(attempt)
    })
  }

  function prepareAttempt({ origin, frame, type, requestKey, presentation, placement, prepare }: AttemptPreparation): CreationAttempt {
    if (!origin.workerId)
      throw new Error('Choose a Worker for the new tab.')
    const key = JSON.stringify([
      frame.account,
      origin.retryKey,
      origin.workspaceId,
      origin.workerId,
      type,
      requestKey,
      presentation
        ? toJsonString(TabPresentationSchema, presentation)
        : '',
    ])
    const previous = attempts.get(key)
    if (previous)
      return previous
    const context: CreationContext = {
      key,
      frame,
      type,
      workerId: origin.workerId,
      creation: create(TabCreationSchema, {
        tabId: randomUUID(),
        placement: placement(),
        ...(presentation
          ? { presentation }
          : {}),
      }),
      controller: new AbortController(),
      sending: false,
      seenLive: false,
      publication: undefined,
      onCommitted: origin.onCommitted,
      observers: new Set(),
    }
    try {
      const attempt: CreationAttempt = Object.assign(context, {
        send: prepare(context),
      })
      attempts.set(key, attempt)
      return attempt
    }
    catch (error) {
      if (accountIsCurrent(frame))
        options.metadata.remove(context.creation.tabId)
      throw error
    }
  }

  function applyAgentReply(attempt: CreationContext, info: AgentInfo, landed: TabWritesLanded, seedFromActive: boolean) {
    if (!accountIsCurrent(attempt.frame) || attemptEnded(attempt))
      return
    const current = options.metadata.get(attempt.creation.tabId)
    if (current?.hydrated)
      return
    const statusChanged = landed.status(info.id)
    const catalogChanged = landed.catalog(info.id)
    const fresh = {
      ...info,
      ...((statusChanged || landed.git(info.id))
        ? { gitStatus: undefined }
        : {}),
      ...(catalogChanged
        ? { optionGroups: current?.optionGroups ?? [] }
        : {}),
    }
    if (!landed.git(info.id) && !fresh.gitStatus && current?.gitToplevel === undefined)
      seedGit(attempt, info.workingDir, seedFromActive)
    const mapped = protoToAgentTabFields(options.repoGitStore, attempt.workerId, fresh)
    const fields = statusChanged
      ? withoutLiveStatusFields(mapped)
      : mapped
    delete fields.workerId
    if (current?.title !== undefined)
      delete fields.title
    options.metadata.patch(info.id, {
      ...fields,
      ...resolveSettingsTabFields(options.view.getAgentTab(info.id), fresh.optionGroups, options.settingsPendingAxes(info.id)),
      hydrated: true,
    })
  }

  function seedGit(attempt: CreationContext, workingDir: string, enabled: boolean, shellStartDir?: string) {
    if (!enabled)
      return
    const seed = planOptimisticRepoGit(options.repoGitStore, attempt.frame.sourceTab, {
      workerId: attempt.workerId,
      workingDir,
      ...(shellStartDir === undefined
        ? {}
        : { shellStartDir }),
    })
    options.metadata.patch(attempt.creation.tabId, seed.fields)
    seed.commit()
  }

  function agentAttempt(input: AgentTabCreation, frame: CreationFrame, request: OpenAgentRequest, placement: () => TabPlacement): CreationAttempt {
    const seedFromActive = input.seedGitFromActiveTab !== false
    return prepareAttempt({
      origin: input,
      frame,
      type: TabType.AGENT,
      requestKey: toJsonString(OpenAgentRequestSchema, request),
      placement,
      prepare: (prepared) => {
        request.creation = prepared.creation
        const landed = watchTabWrites(options.metadata, [prepared.creation.tabId])
        options.metadata.patch(prepared.creation.tabId, {
          agentProvider: request.agentProvider,
          ...(!request.gitSetup
            ? { workingDir: request.workingDir }
            : {}),
          ...(request.title
            ? { title: request.title }
            : {}),
        })
        seedGit(prepared, request.workingDir, seedFromActive)
        return async () => {
          const response = await workerAgent.openAgent(prepared.workerId, request, { signal: prepared.controller.signal })
          const publication = validatePublication(prepared, response.publication)
          if (!response.agent || response.agent.id !== prepared.creation.tabId || response.agent.workerId !== prepared.workerId)
            throw new Error('The Worker returned a different agent identity.')
          applyAgentReply(prepared, response.agent, landed, seedFromActive)
          return publication
        }
      },
    })
  }

  function openAgent(input: AgentTabCreation): Promise<boolean> {
    const frame = captureFrame(input)
    if (!frame)
      return Promise.resolve(false)
    try {
      const request = captureAgentRequest(input.request)
      const attempt = agentAttempt(input, frame, request, () => capturePlacement(input.workspaceId))
      return observe(attempt, input, frame)
    }
    catch (error) {
      if (accountIsCurrent(frame) && viewIsCurrent(frame))
        runLocalCallback(() => input.onError(error))
      return Promise.resolve(false)
    }
  }

  async function openWorkspace(submission: WorkspaceTabCreation): Promise<boolean> {
    const input = { ...submission }
    const frame = captureFrame(input)
    if (!frame)
      return false
    try {
      if (!input.workerId)
        throw new Error('Choose a Worker for the new tab.')
      const request = captureAgentRequest(input.request)
      const title = input.title
      const key = JSON.stringify([
        frame.account,
        input.retryKey,
        input.workerId,
        title,
        toJsonString(OpenAgentRequestSchema, request),
      ])
      let entry = workspaces.get(key)
      if (!entry) {
        const controller = new AbortController()
        const result = Promise.resolve().then(async () => {
          const response = await workspaceClient.createWorkspace({ title }, { signal: controller.signal })
          if (!response.workspaceId)
            throw new Error('The Hub returned no workspace identity.')
          return response.workspaceId
        })
        entry = { controller, result }
        workspaces.set(key, entry)
      }
      const workspace = entry
      const workspaceKey = key
      let workspaceId: string
      try {
        workspaceId = await workspace.result
      }
      catch (error) {
        if (workspaces.get(workspaceKey) === workspace)
          workspaces.delete(workspaceKey)
        throw error
      }
      if (!accountIsCurrent(frame))
        return false
      const origin: AgentTabCreation = { ...input, workspaceId }
      const attempt = agentAttempt(origin, frame, request, () => create(TabPlacementSchema, {
        workspaceId,
        destination: { case: 'firstLeaf', value: {} },
        insertion: TabInsertion.LAST,
      }))
      workspace.attempt = attempt
      return observe(attempt, origin, frame)
    }
    catch (error) {
      if (accountIsCurrent(frame) && viewIsCurrent(frame))
        runLocalCallback(() => input.onError(error))
      return false
    }
  }

  function openTerminal(input: TerminalTabCreation): Promise<boolean> {
    const frame = captureFrame(input)
    if (!frame)
      return Promise.resolve(false)
    try {
      const seedFromActive = input.seedGitFromActiveTab !== false
      const request = clone(OpenTerminalRequestSchema, create(OpenTerminalRequestSchema, input.request))
      request.target = { case: undefined }
      const attempt = prepareAttempt({
        origin: input,
        frame,
        type: TabType.TERMINAL,
        requestKey: toJsonString(OpenTerminalRequestSchema, request),
        placement: () => capturePlacement(input.workspaceId),
        prepare: (prepared) => {
          request.target = { case: 'creation', value: prepared.creation }
          options.metadata.patch(prepared.creation.tabId, {
            ...(!request.gitSetup
              ? {
                  workingDir: request.workingDir,
                  ...(request.shellStartDir
                    ? { shellStartDir: request.shellStartDir }
                    : {}),
                }
              : {}),
            ...(request.title
              ? { title: request.title }
              : {}),
          })
          seedGit(prepared, request.workingDir, seedFromActive, request.shellStartDir || undefined)
          return async () => {
            const response = await workerTerminal.openTerminal(prepared.workerId, request, { signal: prepared.controller.signal })
            const publication = validatePublication(prepared, response.publication)
            if (response.terminalId !== prepared.creation.tabId)
              throw new Error('The Worker returned a different terminal identity.')
            if (accountIsCurrent(prepared.frame) && !attemptEnded(prepared) && options.metadata.get(prepared.creation.tabId)?.title === undefined)
              options.metadata.patch(prepared.creation.tabId, { title: response.title })
            return publication
          }
        },
      })
      return observe(attempt, input, frame)
    }
    catch (error) {
      if (accountIsCurrent(frame) && viewIsCurrent(frame))
        runLocalCallback(() => input.onError(error))
      return Promise.resolve(false)
    }
  }

  function registerPayload(input: PayloadTabCreation): Promise<boolean> {
    const frame = captureFrame(input)
    if (!frame)
      return Promise.resolve(false)
    try {
      const request = clone(RegisterTabPayloadRequestSchema, create(RegisterTabPayloadRequestSchema, input.request))
      request.creation = undefined
      const payload = tabPayloadView(request.payload)
      if (!payload)
        throw new Error('The tab payload is invalid.')
      const type = payload.kind === 'file'
        ? TabType.FILE
        : TabType.IMAGE
      const fileOpenSource = input.fileOpenSource
      const presentation = input.presentation
        ? create(TabPresentationSchema, input.presentation)
        : undefined
      const attempt = prepareAttempt({
        origin: input,
        frame,
        type,
        presentation,
        requestKey: toJsonString(RegisterTabPayloadRequestSchema, request),
        placement: () => capturePlacement(input.workspaceId),
        prepare: (prepared) => {
          request.creation = prepared.creation
          options.metadata.patch(prepared.creation.tabId, {
            ...tabPayloadMetadata(payload),
            ...(payload.kind === 'file'
              ? { title: basename(payload.filePath) || payload.filePath }
              : {}),
            ...(fileOpenSource === undefined
              ? {}
              : { fileOpenSource }),
          })
          seedGit(prepared, request.payload?.workingDir ?? '', true)
          return async () => {
            const response = await workerPrivate.registerTabPayload(prepared.workerId, request, { signal: prepared.controller.signal })
            return validatePublication(prepared, response.publication)
          }
        },
      })
      return observe(attempt, input, frame)
    }
    catch (error) {
      if (accountIsCurrent(frame) && viewIsCurrent(frame))
        runLocalCallback(() => input.onError(error))
      return Promise.resolve(false)
    }
  }

  function acceptsEarlyMetadata(tabId: string, type: TabType, workerId: string): boolean {
    for (const attempt of attempts.values()) {
      if (attempt.creation.tabId !== tabId || attempt.type !== type || attempt.workerId !== workerId
        || !accountIsCurrent(attempt.frame)) {
        continue
      }
      const state = options.state()
      if (state?.userId !== attempt.frame.account)
        return false
      const record = state.tabs[tabId]
      return !record
        ? !attempt.seenLive
        : hlcIsZero(record.tombstoneAt)
          && record.tabType === type && (!record.workerId || record.workerId.value === workerId)
    }
    return false
  }

  return {
    acceptsEarlyMetadata,
    openAgent,
    openTerminal,
    registerPayload,
    openWorkspace,
  }
}

export type TabCreationCoordinator = ReturnType<typeof useTabCreation>
