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
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'

import { SettingsSwitchField } from '../components/settings-form-layout'

export interface StpaySettingsValues {
  StpayEnabled: boolean
  StpayGateway: string
  StpayMerNo: string
  StpayTerNo: string
  StpayHashKey: string
  StpayUnitPrice: number
  StpayMinTopUp: number
  StpayReturnURL: string
  StpayNotifyURL: string
  StpaySiteURL: string
  StpayCardType: string
  StpayRateLimitNum: number
  StpayRateLimitDuration: number
  StpayRequireQueryConfirm: boolean
  StpayNotifyIPAllowlist: string
}

interface Props {
  values: StpaySettingsValues
  onValueChange: <K extends keyof StpaySettingsValues>(
    key: K,
    value: StpaySettingsValues[K]
  ) => void
}

export function StpaySettingsSection(props: Props) {
  const { t } = useTranslation()
  const isTestCardType = props.values.StpayCardType === 'online_banking'

  return (
    <div className='space-y-4 pt-4'>
      <div>
        <h3 className='text-lg font-medium'>{t('Russian SBP')}</h3>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Faster Payments System checkout for Russian payers. Orders always settle in RUB.'
          )}
        </p>
      </div>

      <Alert>
        <AlertDescription className='text-xs'>
          {t(
            'Register the notification URL with the provider. Leaving it blank uses <ServerAddress>/api/stpay/notify. The notification signature carries no timestamp or nonce, so keep the outbound order query enabled below.'
          )}
        </AlertDescription>
      </Alert>

      {isTestCardType ? (
        <Alert variant='destructive'>
          <AlertDescription className='text-xs'>
            {t(
              'Payment type online_banking is only supported by the provider’s test merchants. Paid notifications for it do not credit a wallet unless STPAY_ALLOW_TEST_NOTIFY=true is set on the server.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}

      <div className='grid gap-4 sm:grid-cols-2'>
        <SettingsSwitchField
          checked={props.values.StpayEnabled}
          controlId='stpay-enabled'
          onCheckedChange={(value) =>
            props.onValueChange('StpayEnabled', value)
          }
          label={t('Enable Russian SBP')}
          className='py-0'
        />
        <SettingsSwitchField
          checked={props.values.StpayRequireQueryConfirm}
          controlId='stpay-query-confirm'
          onCheckedChange={(value) =>
            props.onValueChange('StpayRequireQueryConfirm', value)
          }
          label={t('Confirm payments with an order query')}
          description={t(
            'A paid notification credits the wallet only after the provider confirms the order.'
          )}
          className='py-0'
        />
      </div>

      <Separator />

      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-gateway'>{t('Gateway URL')}</Label>
          <Input
            id='stpay-gateway'
            placeholder='https://gateway.example.com'
            value={props.values.StpayGateway}
            onChange={(event) =>
              props.onValueChange('StpayGateway', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-card-type'>{t('Payment type')}</Label>
          <Combobox
            id='stpay-card-type'
            options={[
              { value: 'sbp', label: t('SBP (production)') },
              { value: 'online_banking', label: t('Online banking (test)') },
            ]}
            value={props.values.StpayCardType}
            onValueChange={(value) =>
              props.onValueChange('StpayCardType', value ?? 'sbp')
            }
            className='w-full'
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-mer-no'>{t('Merchant number')}</Label>
          <Input
            id='stpay-mer-no'
            autoComplete='off'
            value={props.values.StpayMerNo}
            onChange={(event) =>
              props.onValueChange('StpayMerNo', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-ter-no'>{t('Terminal number')}</Label>
          <Input
            id='stpay-ter-no'
            autoComplete='off'
            value={props.values.StpayTerNo}
            onChange={(event) =>
              props.onValueChange('StpayTerNo', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-hash-key'>{t('Signature key')}</Label>
          <Input
            id='stpay-hash-key'
            type='password'
            autoComplete='new-password'
            placeholder={t('Leave blank to keep the existing key')}
            value={props.values.StpayHashKey}
            onChange={(event) =>
              props.onValueChange('StpayHashKey', event.target.value)
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('8 to 128 letters or digits.')}
          </p>
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-site-url'>{t('Merchant site host')}</Label>
          <Input
            id='stpay-site-url'
            placeholder='example.com'
            value={props.values.StpaySiteURL}
            onChange={(event) =>
              props.onValueChange('StpaySiteURL', event.target.value)
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('Reported to the provider. Leave blank to use the server host.')}
          </p>
        </div>
      </div>

      <Separator />

      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-unit-price'>{t('Unit price (RUB)')}</Label>
          <Input
            id='stpay-unit-price'
            type='number'
            step={0.1}
            min={0}
            value={props.values.StpayUnitPrice}
            onChange={(event) =>
              props.onValueChange(
                'StpayUnitPrice',
                event.target.value === '' ? 0 : event.target.valueAsNumber
              )
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('Rubles charged for each top-up unit.')}
          </p>
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-min-topup'>
            {t('Minimum top-up quantity')}
          </Label>
          <Input
            id='stpay-min-topup'
            type='number'
            min={1}
            value={props.values.StpayMinTopUp}
            onChange={(event) =>
              props.onValueChange(
                'StpayMinTopUp',
                event.target.value === '' ? 1 : event.target.valueAsNumber
              )
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-notify-url'>
            {t('Callback notification URL')}
          </Label>
          <Input
            id='stpay-notify-url'
            placeholder='https://example.com/api/stpay/notify'
            value={props.values.StpayNotifyURL}
            onChange={(event) =>
              props.onValueChange('StpayNotifyURL', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-return-url'>{t('Payment return URL')}</Label>
          <Input
            id='stpay-return-url'
            placeholder='https://example.com/wallet'
            value={props.values.StpayReturnURL}
            onChange={(event) =>
              props.onValueChange('StpayReturnURL', event.target.value)
            }
          />
        </div>
      </div>

      <Separator />

      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-rate-limit-num'>
            {t('Checkout requests per user')}
          </Label>
          <Input
            id='stpay-rate-limit-num'
            type='number'
            min={0}
            value={props.values.StpayRateLimitNum}
            onChange={(event) =>
              props.onValueChange(
                'StpayRateLimitNum',
                event.target.value === '' ? 0 : event.target.valueAsNumber
              )
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('0 disables the per-user limit.')}
          </p>
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='stpay-rate-limit-duration'>
            {t('Rate limit window (seconds)')}
          </Label>
          <Input
            id='stpay-rate-limit-duration'
            type='number'
            min={1}
            value={props.values.StpayRateLimitDuration}
            onChange={(event) =>
              props.onValueChange(
                'StpayRateLimitDuration',
                event.target.value === '' ? 60 : event.target.valueAsNumber
              )
            }
          />
        </div>
      </div>

      <div className='grid gap-1.5'>
        <Label htmlFor='stpay-allowlist'>
          {t('Notification IP allowlist')}
        </Label>
        <Input
          id='stpay-allowlist'
          placeholder='203.0.113.7, 198.51.100.0/24'
          value={props.values.StpayNotifyIPAllowlist}
          onChange={(event) =>
            props.onValueChange('StpayNotifyIPAllowlist', event.target.value)
          }
        />
        <p className='text-muted-foreground text-xs'>
          {t(
            'Comma-separated IP addresses or CIDR ranges allowed to send notifications. Leave blank to accept any source.'
          )}
        </p>
      </div>
    </div>
  )
}
