import { toast } from 'sonner'
import { getFriendlyErrorMessage } from './errorParser'

export function showError(error) {
  const message = getFriendlyErrorMessage(error)
  toast.error(message)
}

export function showSuccess(message) {
  toast.success(message)
}

export function showInfo(message) {
  toast(message)
}
