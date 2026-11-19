import type { ProviderControlCapability } from '../capabilities'
import { piControlResponseSummary } from './controlResponse'

/** The Pi control facts that the browser states, separate from provider registration. */
export const piControls: ProviderControlCapability = {
  controlResponseDisplay: piControlResponseSummary,
}
