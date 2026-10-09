import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { classifyMuseMessage, museSpanRole } from './classification'
import { museControl } from './control'
import { museCompactionBoundary, museNotificationEntry } from './extractors/notification'
import { museOutputFilePaths } from './extractors/outputFilePaths'
import { museResultDivider } from './extractors/resultDivider'
import { museExtractRow } from './extractors/row'
import { musePermissionPresets } from './permissionPresets'
import { museConfiguration } from './pluginConfiguration'
import './plugin'

describe('musePlugin', () => {
  it('registers every provider extraction role directly', () => {
    const plugin = providerFor(AgentProvider.MUSE_CODE)
    expect(plugin?.transcript.classify).toBe(classifyMuseMessage)
    expect(plugin?.transcript.spanRole).toBe(museSpanRole)
    expect(plugin?.transcript.extractRow).toBe(museExtractRow)
    expect(plugin?.transcript.extractDivider).toBe(museResultDivider)
    expect(plugin?.transcript.notificationEntry).toBe(museNotificationEntry)
    expect(plugin?.transcript.outputFilePaths).toBe(museOutputFilePaths)
  })

  it('registers the native controls and startup configuration', () => {
    const plugin = providerFor(AgentProvider.MUSE_CODE)
    expect(plugin?.controls?.askUserQuestion).toBe(museControl.askUserQuestion)
    expect(plugin?.controls?.extractControl).toBe(museControl.extractControl)
    expect(plugin?.controls?.permissionPresets).toBe(musePermissionPresets)
    expect(plugin?.configuration).toBe(museConfiguration)
  })

  it('uses normalized Worker usage with the native compaction hook', () => {
    const plugin = providerFor(AgentProvider.MUSE_CODE)
    expect(plugin?.session?.compactionBoundaryFromMessage).toBe(museCompactionBoundary)
    expect(plugin?.session?.contextUsageFromMessage).toBeUndefined()
  })
})
