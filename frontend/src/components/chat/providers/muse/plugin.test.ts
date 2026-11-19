import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { classifyMuseMessage, museSpanRole } from './classification'
import { museControlResponseSummary } from './controlResponse'
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

  it('needs no request for any row, because every native item states its tool and arguments', () => {
    expect(providerFor(AgentProvider.MUSE_CODE)?.transcript.needsRequest).toBeUndefined()
  })

  // The Worker reads each request and builds each answer, so the browser keeps only the
  // display of a saved answer and the presets.
  it('registers the saved-answer display, the presets and the startup configuration', () => {
    const plugin = providerFor(AgentProvider.MUSE_CODE)
    expect(Object.keys(plugin?.controls ?? {}).sort()).toEqual(['controlResponseDisplay', 'permissionPresets'])
    expect(plugin?.controls?.controlResponseDisplay).toBe(museControlResponseSummary)
    expect(plugin?.controls?.permissionPresets).toBe(musePermissionPresets)
    expect(plugin?.configuration).toBe(museConfiguration)
  })

  it('uses normalized Worker usage with the native compaction hook', () => {
    const plugin = providerFor(AgentProvider.MUSE_CODE)
    expect(plugin?.session?.compactionBoundaryFromMessage).toBe(museCompactionBoundary)
    expect(plugin?.session?.contextUsageFromMessage).toBeUndefined()
  })
})
