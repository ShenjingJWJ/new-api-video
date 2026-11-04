/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import i18next from 'i18next'
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'

import { requestStpayPayment, isApiSuccess } from '../api'
import { getPaymentErrorTranslation } from '../lib'
import type { StpayPaymentRequest } from '../types'
import { getGatewayErrorMessage } from './gateway-error'

/**
 * Reject non-navigable schemes (e.g. javascript:, data:) and relative URLs.
 * Only http/https are allowed for backend-provided redirect targets.
 */
function isSafeHttpCheckoutUrl(value: string): boolean {
  const trimmed = value.trim()
  if (!trimmed) {
    return false
  }
  try {
    const url = new URL(trimmed)
    return url.protocol === 'http:' || url.protocol === 'https:'
  } catch {
    return false
  }
}

/**
 * Hook for the Russian SBP (Stpay) checkout flow.
 *
 * Same-tab redirect rather than window.open: the user-gesture context is lost
 * across the await, so popups get blocked.
 */
export function useStpayPayment() {
  const [processing, setProcessing] = useState(false)

  const processStpayPayment = useCallback(
    async (request: StpayPaymentRequest) => {
      setProcessing(true)

      try {
        const response = await requestStpayPayment(request)
        const paymentUrl = response.data?.payment_url

        if (isApiSuccess(response) && paymentUrl) {
          if (!isSafeHttpCheckoutUrl(paymentUrl)) {
            toast.error(i18next.t('Invalid payment redirect URL'))
            return false
          }
          toast.success(i18next.t('Redirecting to payment page...'))
          window.location.href = paymentUrl
          return true
        }

        handleServerError(response, i18next.t('Payment request failed'), {
          title:
            getPaymentErrorTranslation(response) ??
            getGatewayErrorMessage(response.message, response.data),
        })
        return false
      } catch (error) {
        handleServerError(error, i18next.t('Payment request failed'))
        return false
      } finally {
        setProcessing(false)
      }
    },
    []
  )

  return { processing, processStpayPayment }
}
