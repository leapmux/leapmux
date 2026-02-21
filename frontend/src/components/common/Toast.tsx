export type ToastType = 'danger' | 'success'

export function showToast(message: string, type: ToastType = 'danger') {
  window.ot.toast(message, '', {
    variant: type === 'success' ? 'success' : 'danger',
    placement: 'top-right',
    duration: 5000,
  })
}
