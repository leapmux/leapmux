import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import { junieOutputFilePaths } from './extractors/outputFilePaths'

import './plugin'

describe('junie provider', () => {
  describeACPProviderBasics(AgentProvider.JUNIE, { text: true, image: true, pdf: false, binary: false })

  it('registers its own reader of output file paths', () => {
    expect(providerFor(AgentProvider.JUNIE)?.transcript.outputFilePaths).toBe(junieOutputFilePaths)
  })

  it('carries plan mode on the permission-mode axis', () => {
    const configuration = providerFor(AgentProvider.JUNIE)?.configuration
    expect(configuration?.triggerModeGroupKey).toBe(OPTION_ID_PERMISSION_MODE)
    expect(configuration?.planMode).toBeDefined()
  })
})
