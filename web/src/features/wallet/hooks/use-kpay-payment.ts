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
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'

import { requestKpayPayment, isApiSuccess } from '../api'
import { getPaymentErrorTranslation } from '../lib'
import type { KpayPaymentRequest } from '../types'
import { getGatewayErrorMessage } from './gateway-error'

/**
 * KPay accepts only these three checkout languages, so the interface language
 * is mapped onto them instead of being forwarded as a BCP 47 tag.
 */
export function toKpayCheckoutLanguage(language: string): string {
  if (!language.toLowerCase().startsWith('zh')) {
    return 'en_US'
  }
  return /TW|HK|Hant/i.test(language) ? 'zh_HK' : 'zh_CN'
}

/**
 * Hook for the KPay hosted-checkout flow.
 *
 * The tab is opened before the request so the user gesture is still in effect;
 * opening it after the await would be blocked as a popup.
 */
export function useKpayPayment() {
  const { i18n } = useTranslation()
  const [processing, setProcessing] = useState(false)

  const processKpayPayment = useCallback(
    async (request: KpayPaymentRequest) => {
      const checkout = window.open('about:blank', '_blank')
      if (!checkout) {
        toast.error(i18next.t('Allow pop-ups to open the payment page'))
        return false
      }
      checkout.opener = null
      setProcessing(true)

      try {
        const response = await requestKpayPayment({
          ...request,
          language: toKpayCheckoutLanguage(
            i18n.resolvedLanguage || i18n.language
          ),
        })

        const paymentUrl = response.data?.payment_url
        // KPay checkout pages are always HTTPS; anything else is rejected
        // rather than navigated to.
        if (isApiSuccess(response) && paymentUrl?.startsWith('https://')) {
          checkout.location.href = paymentUrl
          toast.success(i18next.t('Redirecting to payment page...'))
          return true
        }

        checkout.close()
        handleServerError(response, i18next.t('Payment request failed'), {
          title:
            getPaymentErrorTranslation(response) ??
            getGatewayErrorMessage(response.message, response.data),
        })
        return false
      } catch (error) {
        checkout.close()
        handleServerError(error, i18next.t('Payment request failed'))
        return false
      } finally {
        setProcessing(false)
      }
    },
    [i18n.language, i18n.resolvedLanguage]
  )

  return { processing, processKpayPayment }
}
