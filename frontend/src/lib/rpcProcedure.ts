import type { DescMethod } from '@bufbuild/protobuf'

/**
 * The procedure path an inner RPC travels under inside an E2EE channel:
 * `/leapmux.v1.<Service>/<Method>`.
 *
 * It is the Connect procedure path of the method's declaration, so the worker,
 * which registers each handler under the generated `leapmuxv1connect` constant
 * for the same method, reads the same string. Derive it from the generated
 * method descriptor, never spell it by hand.
 */
export function procedureOf(method: DescMethod): string {
  return `/${method.parent.typeName}/${method.name}`
}
