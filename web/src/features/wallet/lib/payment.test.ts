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
import { describe, expect, test } from 'vitest'

import { PAYMENT_TYPES } from '../constants'
import {
  dispatchSelectedPayment,
  getPaymentCurrency,
  isStripePayment,
  isWaffoPayment,
  isWaffoPancakePayment,
} from './payment'

describe('payment type classification', () => {
  test('keeps Waffo and Waffo Pancake on their dedicated flows', () => {
    expect(isWaffoPayment(PAYMENT_TYPES.WAFFO)).toBe(true)
    expect(isWaffoPayment(PAYMENT_TYPES.WAFFO_PANCAKE)).toBe(false)
    expect(isWaffoPancakePayment(PAYMENT_TYPES.WAFFO_PANCAKE)).toBe(true)
    expect(isWaffoPancakePayment(PAYMENT_TYPES.WAFFO)).toBe(false)
    expect(isStripePayment(PAYMENT_TYPES.STRIPE)).toBe(true)
  })

  test('reports the settlement currency of the hosted gateways', () => {
    expect(getPaymentCurrency(PAYMENT_TYPES.KPAY)).toBe('HKD')
    expect(getPaymentCurrency(PAYMENT_TYPES.STPAY)).toBe('RUB')
    expect(getPaymentCurrency('kpay_tokens')).toBe('HKD')
    expect(getPaymentCurrency('stpay_sbp_tokens')).toBe('RUB')
    expect(getPaymentCurrency(PAYMENT_TYPES.ALIPAY)).toBeUndefined()
  })
})

describe('payment dispatch', () => {
  const processors = (calls: string[]) => ({
    regular: async () => {
      calls.push('regular')
      return false
    },
    waffo: async (amount: number, index: number) => {
      calls.push(`waffo:${amount}:${index}`)
      return true
    },
    waffoPancake: async () => {
      calls.push('pancake')
      return false
    },
    kpay: async (amount: number) => {
      calls.push(`kpay:${amount}`)
      return true
    },
    stpay: async (amount: number) => {
      calls.push(`stpay:${amount}`)
      return true
    },
  })

  test('keeps the selected Waffo method index through confirmation', async () => {
    const calls: string[] = []
    const success = await dispatchSelectedPayment(
      { name: 'Waffo Card', type: PAYMENT_TYPES.WAFFO },
      120,
      3,
      processors(calls)
    )

    expect(success).toBe(true)
    expect(calls).toEqual(['waffo:120:3'])
  })

  test('does not create a Waffo order without a selected method index', async () => {
    const calls: string[] = []
    const success = await dispatchSelectedPayment(
      { name: 'Waffo Card', type: PAYMENT_TYPES.WAFFO },
      120,
      null,
      processors(calls)
    )

    expect(success).toBe(false)
    expect(calls).toEqual([])
  })

  test('sends KPay to its hosted checkout instead of the epay form', async () => {
    const calls: string[] = []
    const success = await dispatchSelectedPayment(
      { name: 'KPay', type: PAYMENT_TYPES.KPAY },
      120,
      null,
      processors(calls)
    )

    expect(success).toBe(true)
    expect(calls).toEqual(['kpay:120'])
  })

  test('sends Russian SBP to its own checkout instead of the epay form', async () => {
    const calls: string[] = []
    const success = await dispatchSelectedPayment(
      { name: 'Russian SBP', type: PAYMENT_TYPES.STPAY },
      120,
      null,
      processors(calls)
    )

    expect(success).toBe(true)
    expect(calls).toEqual(['stpay:120'])
  })
})
