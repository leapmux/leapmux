/** Type declarations for the Oat UI global `ot` object. */
declare global {
  interface Window {
    ot: {
      toast: ((message: string, title?: string, options?: {
        variant?: 'success' | 'danger' | 'warning' | ''
        placement?: 'top-left' | 'top-center' | 'top-right' | 'bottom-left' | 'bottom-center' | 'bottom-right'
        duration?: number
      }) => void) & { clear: (placement?: string) => void }
      toastEl: (element: HTMLElement, options?: Record<string, unknown>) => void
    }
  }
}

export {}
