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
import { Checkbox } from '@/components/ui/checkbox'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'

import { SettingsSwitchField } from '../components/settings-form-layout'

export interface KpaySettingsValues {
  KpayEnabled: boolean
  KpaySandbox: boolean
  KpayMerchantCode: string
  KpayAppID: string
  KpayPrivateKey: string
  KpayPlatformPublicKey: string
  KpaySandboxMerchantCode: string
  KpaySandboxAppID: string
  KpaySandboxPrivateKey: string
  KpaySandboxPlatformPublicKey: string
  KpayUnitPrice: number
  KpayMinTopUp: number
  KpayNotifyURL: string
  KpayReturnURL: string
  KpayLanguage: string
  KpayCheckoutMode: string
  KpayMerchantIcon: string
  KpayOrderRemark: string
  KpayPayMethodOrder: string
  KpayPageFeatureControls: string
  KpayRateLimitNum: number
  KpayRateLimitDuration: number
  KpayReconcileInterval: number
}

interface Props {
  values: KpaySettingsValues
  onValueChange: <K extends keyof KpaySettingsValues>(
    key: K,
    value: KpaySettingsValues[K]
  ) => void
}

/** Checkout methods KPay accepts in payMethodOrder. */
const KPAY_PAY_METHODS = [
  'CARD',
  'ALIPAYCN',
  'ALIPAYHK',
  'WXPAY',
  'UNIONPAY',
  'PAYME',
  'FPS',
  'APPLEPAY',
  'GOOGLEPAY',
  'OCTOPUS',
]

/** Checkout page toggles KPay accepts in pageFeatureControls. */
const KPAY_PAGE_FEATURES = [
  'HIDE_REMAINING_PAYMENT_TIME',
  'HIDE_ORDER_ITEM_DETAILS_BUTTON',
  'HIDE_BIND_CARD_PAY_BUTTON',
  'MUST_ENABLE_BIND_CARD_PAY_BUTTON',
  'HIDE_EMAIL_ADDRESS',
  'HIDE_BILL_INFORMATION',
]

/** The two card-binding toggles contradict each other and KPay rejects both. */
const KPAY_EXCLUSIVE_FEATURES: Record<string, string> = {
  HIDE_BIND_CARD_PAY_BUTTON: 'MUST_ENABLE_BIND_CARD_PAY_BUTTON',
  MUST_ENABLE_BIND_CARD_PAY_BUTTON: 'HIDE_BIND_CARD_PAY_BUTTON',
}

function parseStringArray(value: string): string[] {
  try {
    const parsed: unknown = JSON.parse(value || '[]')
    if (!Array.isArray(parsed)) return []
    return parsed.filter((entry): entry is string => typeof entry === 'string')
  } catch {
    return []
  }
}

export function KpaySettingsSection(props: Props) {
  const { t } = useTranslation()

  const payMethodOrder = parseStringArray(props.values.KpayPayMethodOrder)
  const pageFeatures = parseStringArray(props.values.KpayPageFeatureControls)

  // Selection order is the display order KPay renders, so a newly checked
  // method is appended rather than inserted at its position in the catalog.
  const togglePayMethod = (method: string, checked: boolean) => {
    const remaining = payMethodOrder.filter((entry) => entry !== method)
    props.onValueChange(
      'KpayPayMethodOrder',
      JSON.stringify(checked ? [...remaining, method] : remaining)
    )
  }

  const togglePageFeature = (feature: string, checked: boolean) => {
    const excluded = KPAY_EXCLUSIVE_FEATURES[feature]
    const remaining = pageFeatures.filter(
      (entry) => entry !== feature && (!checked || entry !== excluded)
    )
    props.onValueChange(
      'KpayPageFeatureControls',
      JSON.stringify(checked ? [...remaining, feature] : remaining)
    )
  }

  const sandbox = props.values.KpaySandbox

  return (
    <div className='space-y-4 pt-4'>
      <div>
        <h3 className='text-lg font-medium'>{t('KPay Hosted Checkout')}</h3>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Hong Kong hosted checkout settling in HKD. Cards, Alipay HK, WeChat Pay, FPS, PayMe, Apple Pay and Google Pay are offered on KPay’s own payment page.'
          )}
        </p>
      </div>

      <Alert>
        <AlertDescription className='text-xs'>
          {t(
            'Register the notification URL in the KPay merchant console. Leaving it blank uses <ServerAddress>/api/kpay/notify. Live and sandbox credentials are stored side by side, and each order records the environment it was created in, so switching sandbox mode never re-routes an open order.'
          )}
        </AlertDescription>
      </Alert>

      <div className='grid gap-4 sm:grid-cols-2'>
        <SettingsSwitchField
          checked={props.values.KpayEnabled}
          controlId='kpay-enabled'
          onCheckedChange={(value) => props.onValueChange('KpayEnabled', value)}
          label={t('Enable KPay')}
          className='py-0'
        />
        <SettingsSwitchField
          checked={sandbox}
          controlId='kpay-sandbox'
          onCheckedChange={(value) => props.onValueChange('KpaySandbox', value)}
          label={t('Sandbox mode')}
          description={t('New orders use the sandbox credentials below.')}
          className='py-0'
        />
      </div>

      <Separator />

      <h4 className='font-medium'>
        {sandbox
          ? t('Production credentials (standby)')
          : t('Production credentials (in use)')}
      </h4>

      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-merchant-code'>{t('Merchant code')}</Label>
          <Input
            id='kpay-merchant-code'
            autoComplete='off'
            value={props.values.KpayMerchantCode}
            onChange={(event) =>
              props.onValueChange('KpayMerchantCode', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-app-id'>{t('App ID')}</Label>
          <Input
            id='kpay-app-id'
            autoComplete='off'
            value={props.values.KpayAppID}
            onChange={(event) =>
              props.onValueChange('KpayAppID', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-private-key'>{t('Merchant private key')}</Label>
          <Textarea
            id='kpay-private-key'
            rows={3}
            autoComplete='new-password'
            placeholder={t('Leave blank to keep the existing key')}
            value={props.values.KpayPrivateKey}
            onChange={(event) =>
              props.onValueChange('KpayPrivateKey', event.target.value)
            }
            className='font-mono text-xs'
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-platform-key'>
            {t('KPay platform public key')}
          </Label>
          <Textarea
            id='kpay-platform-key'
            rows={3}
            autoComplete='off'
            placeholder={t('Leave blank to keep the existing key')}
            value={props.values.KpayPlatformPublicKey}
            onChange={(event) =>
              props.onValueChange('KpayPlatformPublicKey', event.target.value)
            }
            className='font-mono text-xs'
          />
        </div>
      </div>

      <h4 className='font-medium'>
        {sandbox
          ? t('Sandbox credentials (in use)')
          : t('Sandbox credentials (standby)')}
      </h4>

      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-sandbox-merchant-code'>
            {t('Sandbox merchant code')}
          </Label>
          <Input
            id='kpay-sandbox-merchant-code'
            autoComplete='off'
            value={props.values.KpaySandboxMerchantCode}
            onChange={(event) =>
              props.onValueChange('KpaySandboxMerchantCode', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-sandbox-app-id'>{t('Sandbox app ID')}</Label>
          <Input
            id='kpay-sandbox-app-id'
            autoComplete='off'
            value={props.values.KpaySandboxAppID}
            onChange={(event) =>
              props.onValueChange('KpaySandboxAppID', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-sandbox-private-key'>
            {t('Sandbox merchant private key')}
          </Label>
          <Textarea
            id='kpay-sandbox-private-key'
            rows={3}
            autoComplete='new-password'
            placeholder={t('Leave blank to keep the existing key')}
            value={props.values.KpaySandboxPrivateKey}
            onChange={(event) =>
              props.onValueChange('KpaySandboxPrivateKey', event.target.value)
            }
            className='font-mono text-xs'
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-sandbox-platform-key'>
            {t('Sandbox KPay platform public key')}
          </Label>
          <Textarea
            id='kpay-sandbox-platform-key'
            rows={3}
            autoComplete='off'
            placeholder={t('Leave blank to keep the existing key')}
            value={props.values.KpaySandboxPlatformPublicKey}
            onChange={(event) =>
              props.onValueChange(
                'KpaySandboxPlatformPublicKey',
                event.target.value
              )
            }
            className='font-mono text-xs'
          />
        </div>
      </div>

      <Separator />

      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-unit-price'>{t('Unit price (HKD)')}</Label>
          <Input
            id='kpay-unit-price'
            type='number'
            step={0.1}
            min={0}
            value={props.values.KpayUnitPrice}
            onChange={(event) =>
              props.onValueChange(
                'KpayUnitPrice',
                event.target.value === '' ? 0 : event.target.valueAsNumber
              )
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('Hong Kong dollars charged for each top-up unit.')}
          </p>
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-min-topup'>{t('Minimum top-up quantity')}</Label>
          <Input
            id='kpay-min-topup'
            type='number'
            min={1}
            value={props.values.KpayMinTopUp}
            onChange={(event) =>
              props.onValueChange(
                'KpayMinTopUp',
                event.target.value === '' ? 1 : event.target.valueAsNumber
              )
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-notify-url'>
            {t('Callback notification URL')}
          </Label>
          <Input
            id='kpay-notify-url'
            placeholder='https://example.com/api/kpay/notify'
            value={props.values.KpayNotifyURL}
            onChange={(event) =>
              props.onValueChange('KpayNotifyURL', event.target.value)
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'Must be a public HTTPS path. Leave blank to use the server address.'
            )}
          </p>
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-return-url'>{t('Payment return URL')}</Label>
          <Input
            id='kpay-return-url'
            placeholder='https://example.com/wallet'
            value={props.values.KpayReturnURL}
            onChange={(event) =>
              props.onValueChange('KpayReturnURL', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-language'>{t('Checkout page language')}</Label>
          <Combobox
            id='kpay-language'
            options={[
              { value: 'zh_HK', label: t('Traditional Chinese (Hong Kong)') },
              { value: 'zh_CN', label: t('Simplified Chinese') },
              { value: 'en_US', label: t('English') },
            ]}
            value={props.values.KpayLanguage}
            onValueChange={(value) =>
              props.onValueChange('KpayLanguage', value ?? 'zh_HK')
            }
            className='w-full'
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-checkout-mode'>{t('Checkout page type')}</Label>
          <Combobox
            id='kpay-checkout-mode'
            options={[
              { value: 'auto', label: t('Auto (by device)') },
              { value: 'web', label: t('Desktop page') },
              { value: 'h5', label: t('Mobile page') },
            ]}
            value={props.values.KpayCheckoutMode}
            onValueChange={(value) =>
              props.onValueChange('KpayCheckoutMode', value ?? 'auto')
            }
            className='w-full'
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-merchant-icon'>{t('Merchant icon URL')}</Label>
          <Input
            id='kpay-merchant-icon'
            placeholder='https://example.com/logo.png'
            value={props.values.KpayMerchantIcon}
            onChange={(event) =>
              props.onValueChange('KpayMerchantIcon', event.target.value)
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-order-remark'>{t('Order remark')}</Label>
          <Input
            id='kpay-order-remark'
            value={props.values.KpayOrderRemark}
            onChange={(event) =>
              props.onValueChange('KpayOrderRemark', event.target.value)
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('Shown on the KPay checkout page. Up to 255 characters.')}
          </p>
        </div>
      </div>

      <Separator />

      <div className='space-y-2'>
        <Label>{t('Checkout payment methods')}</Label>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Methods appear on the checkout page in the order you select them. Select none to use the order configured on the KPay side.'
          )}
        </p>
        <div className='grid gap-2 sm:grid-cols-3 lg:grid-cols-5'>
          {KPAY_PAY_METHODS.map((method) => (
            <Label
              key={method}
              className='flex items-center gap-2 text-xs font-normal'
            >
              <Checkbox
                checked={payMethodOrder.includes(method)}
                onCheckedChange={(checked) =>
                  togglePayMethod(method, checked === true)
                }
              />
              {method}
            </Label>
          ))}
        </div>
      </div>

      <div className='space-y-2'>
        <Label>{t('Checkout page controls')}</Label>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Hide or require elements on the KPay checkout page. Hiding and requiring card binding cannot both be enabled.'
          )}
        </p>
        <div className='grid gap-2 sm:grid-cols-2 lg:grid-cols-3'>
          {KPAY_PAGE_FEATURES.map((feature) => (
            <Label
              key={feature}
              className='flex items-center gap-2 text-xs font-normal'
            >
              <Checkbox
                checked={pageFeatures.includes(feature)}
                onCheckedChange={(checked) =>
                  togglePageFeature(feature, checked === true)
                }
              />
              {feature}
            </Label>
          ))}
        </div>
      </div>

      <Separator />

      <div className='grid gap-4 sm:grid-cols-3'>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-rate-limit-num'>
            {t('Checkout requests per user')}
          </Label>
          <Input
            id='kpay-rate-limit-num'
            type='number'
            min={0}
            value={props.values.KpayRateLimitNum}
            onChange={(event) =>
              props.onValueChange(
                'KpayRateLimitNum',
                event.target.value === '' ? 0 : event.target.valueAsNumber
              )
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('0 disables the per-user limit.')}
          </p>
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-rate-limit-duration'>
            {t('Rate limit window (seconds)')}
          </Label>
          <Input
            id='kpay-rate-limit-duration'
            type='number'
            min={1}
            value={props.values.KpayRateLimitDuration}
            onChange={(event) =>
              props.onValueChange(
                'KpayRateLimitDuration',
                event.target.value === '' ? 60 : event.target.valueAsNumber
              )
            }
          />
        </div>
        <div className='grid gap-1.5'>
          <Label htmlFor='kpay-reconcile-interval'>
            {t('Reconciliation interval (seconds)')}
          </Label>
          <Input
            id='kpay-reconcile-interval'
            type='number'
            min={30}
            value={props.values.KpayReconcileInterval}
            onChange={(event) =>
              props.onValueChange(
                'KpayReconcileInterval',
                event.target.value === '' ? 120 : event.target.valueAsNumber
              )
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('Shortest gap between two status queries of the same order.')}
          </p>
        </div>
      </div>
    </div>
  )
}
