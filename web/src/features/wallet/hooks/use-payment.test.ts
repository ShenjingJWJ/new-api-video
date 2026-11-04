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
import type { AmountRequest } from '../types'
import { requestPaymentAmount } from './use-payment'

describe('payment amount routing', () => {
  const calculators = (calls: string[]) => ({
    regular: async () => {
      calls.push('regular')
      return { success: true, data: '1' }
    },
    stripe: async () => {
      calls.push('stripe')
      return { success: true, data: '2' }
    },
    waffo: async (request: AmountRequest) => {
      calls.push(`waffo:${request.amount}`)
      return { success: true, data: '18.75' }
    },
    waffoPancake: async () => {
      calls.push('pancake')
      return { success: true, data: '4' }
    },
    kpay: async (request: AmountRequest) => {
      calls.push(`kpay:${request.amount}`)
      return { success: true, data: '936.00' }
    },
    stpay: async (request: AmountRequest) => {
      calls.push(`stpay:${request.amount}`)
      return { success: true, data: '9600.00' }
    },
  })

  test('uses the dedicated Waffo amount calculator', async () => {
    const calls: string[] = []
    const amount = await requestPaymentAmount(
      120,
      PAYMENT_TYPES.WAFFO,
      calculators(calls)
    )

    expect(amount).toBe(18.75)
    expect(calls).toEqual(['waffo:120'])
  })

  test('quotes KPay in HKD through its own calculator', async () => {
    const calls: string[] = []
    const amount = await requestPaymentAmount(
      120,
      PAYMENT_TYPES.KPAY,
      calculators(calls)
    )

    expect(amount).toBe(936)
    expect(calls).toEqual(['kpay:120'])
  })

  test('quotes Russian SBP in RUB through its own calculator', async () => {
    const calls: string[] = []
    const amount = await requestPaymentAmount(
      120,
      PAYMENT_TYPES.STPAY,
      calculators(calls)
    )

    expect(amount).toBe(9600)
    expect(calls).toEqual(['stpay:120'])
  })
})
