import type { ProviderControlCapability } from '../capabilities'
import { qoderExtractControl } from './extractControl'

/**
 * The Qoder control channel.
 *
 * The browser sends the neutral behavior envelope and the worker translates it
 * into Qoder's `{behavior, outcome}` answer, so the shared Allow/Deny pair is
 * the surface.
 */
export const qoderControls: ProviderControlCapability = {
  extractControl: qoderExtractControl,
}
