/** Type declarations for the Oat UI global `ot` object. */
declare global {
  interface Window {
    ot: {
      toast: ((message: string, title?: string, options?: {
        variant?: 'success' | 'danger' | 'warning' | ''
        placement?: 'top-left' | 'top-center' | 'top-right' | 'bottom-left' | 'bottom-center' | 'bottom-right'
        duration?: number
      }) => void) & {
        // Returns the node it actually mounted, which is a CLONE of `element`
        // -- not `element` itself. Callers that need to reach the live toast
        // (to wire a handler onto it, say) must use the return value.
        el: (element: HTMLElement, options?: Record<string, unknown>) => HTMLElement | undefined
        clear: (placement?: string) => void
      }
    }
  }
}

export {}
