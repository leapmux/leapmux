import { createLogger } from '~/lib/logger'

const log = createLogger('toast')

export type ToastType = 'danger' | 'success'

/** Show a warning toast and log the error at warn level. */
export function showWarnToast(message: string, err?: unknown) {
  log.warn(message, err)
  showToast(err instanceof Error ? err.message : message, 'danger')
}

/** Show an error toast and log the error at error level. */
export function showErrorToast(message: string, err?: unknown) {
  log.error(message, err)
  showToast(err instanceof Error ? err.message : message, 'danger')
}

export function showToast(message: string, type: ToastType = 'success') {
  const variant = type === 'success' ? 'success' : 'danger'

  const toast = document.createElement('output')
  toast.setAttribute('data-variant', variant)
  toast.style.display = 'flex'
  toast.style.alignItems = 'start'
  toast.style.gap = 'var(--space-3)'

  const msgEl = document.createElement('div')
  msgEl.className = 'toast-message'
  msgEl.style.flex = '1'
  msgEl.textContent = message
  toast.appendChild(msgEl)

  const closeBtn = document.createElement('button')
  closeBtn.setAttribute('data-close', '')
  closeBtn.textContent = '\u00D7'
  closeBtn.onclick = () => toast.remove()
  toast.appendChild(closeBtn)

  window.ot.toastEl(toast, {
    placement: 'bottom-right',
    duration: 3000,
  })
}
