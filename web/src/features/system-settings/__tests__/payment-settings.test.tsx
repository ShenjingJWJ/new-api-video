import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { BillingSettings } from '../billing'
import { SettingsPageProvider } from '../components/settings-page-context'
import type { BillingSettings as BillingSettingsValues } from '../types'

const { saveOption } = vi.hoisted(() => ({
  saveOption: vi.fn().mockResolvedValue({ success: true }),
}))

vi.mock('../hooks/use-update-option', () => ({
  useUpdateOption: () => ({ mutateAsync: saveOption, isPending: false }),
}))

vi.mock('../components/settings-page', () => ({
  SettingsPage: (props: {
    defaultSettings: BillingSettingsValues
    getSectionContent: (
      section: 'payment',
      settings: BillingSettingsValues
    ) => ReactNode
  }) =>
    props.getSectionContent('payment', {
      ...props.defaultSettings,
      PayMethods: '[]',
      'payment_setting.amount_options': '[]',
      'payment_setting.amount_discount': '{}',
      'payment_setting.compliance_confirmed': true,
      'payment_setting.compliance_terms_version': 'v1',
    }),
}))

vi.mock('../integrations/waffo-settings-section', () => ({
  WaffoSettingsSection: () => null,
}))
vi.mock('../integrations/waffo-pancake-settings-section', () => ({
  WaffoPancakeSettingsSection: () => null,
}))

afterEach(() => {
  cleanup()
  document.body.replaceChildren()
})

function renderPaymentSettings() {
  const actions = document.createElement('div')
  document.body.append(actions)
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SettingsPageProvider actionsContainer={actions}>
        <BillingSettings />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
  return userEvent.setup()
}

describe('payment gateway settings', () => {
  it('keeps production and sandbox credential headings matched when switching environments', async () => {
    const user = renderPaymentSettings()
    await user.click(screen.getByRole('tab', { name: 'KPay' }))
    expect(
      screen
        .getAllByRole('heading', { level: 4 })
        .map((heading) => heading.textContent)
    ).toEqual([
      'Production credentials (standby)',
      'Sandbox credentials (in use)',
    ])
    await user.click(screen.getByRole('switch', { name: 'Sandbox mode' }))
    expect(
      screen
        .getAllByRole('heading', { level: 4 })
        .map((heading) => heading.textContent)
    ).toEqual([
      'Production credentials (in use)',
      'Sandbox credentials (standby)',
    ])
    expect(screen.getByLabelText('Merchant code')).toBeInTheDocument()
  })

  it('shows the KPay tab and saves credentials before enabling the gateway', async () => {
    const user = renderPaymentSettings()
    await user.click(screen.getByRole('tab', { name: 'KPay' }))
    await user.type(screen.getByLabelText('Merchant code'), 'merchant-123')
    await user.click(screen.getByRole('switch', { name: 'Enable KPay' }))
    await user.click(screen.getByRole('button', { name: 'Save all settings' }))
    await waitFor(() => expect(saveOption).toHaveBeenCalledTimes(2))
    expect(saveOption.mock.calls.map(([request]) => request)).toEqual([
      { key: 'KpayMerchantCode', value: 'merchant-123' },
      { key: 'KpayEnabled', value: true },
    ])
  })

  it('shows Russian SBP settings and preserves an empty stored secret on save', async () => {
    const user = renderPaymentSettings()
    await user.click(screen.getByRole('tab', { name: 'Russian SBP' }))
    await user.type(screen.getByLabelText('Merchant number'), 'merchant-456')
    await user.click(screen.getByRole('button', { name: 'Save all settings' }))
    await waitFor(() => expect(saveOption).toHaveBeenCalledTimes(1))
    expect(saveOption).toHaveBeenCalledWith({
      key: 'StpayMerNo',
      value: 'merchant-456',
    })
  })
})
