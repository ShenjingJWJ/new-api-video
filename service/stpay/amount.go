package stpay

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// FormatAmount sends a positive decimal amount. RUB is not in the provider's
// documented zero-decimal set, and the signed examples use two decimals, so
// two decimals are preserved even when the value is whole.
func FormatAmount(amount decimal.Decimal) (string, error) {
	if !amount.IsPositive() {
		return "", fmt.Errorf("stpay amount must be positive")
	}
	formatted := amount.Round(2).StringFixed(2)
	if formatted == "0.00" {
		return "", fmt.Errorf("stpay amount must be positive")
	}
	return formatted, nil
}

func SameAmount(left, right string) bool {
	a, errA := decimal.NewFromString(strings.TrimSpace(left))
	b, errB := decimal.NewFromString(strings.TrimSpace(right))
	if errA != nil || errB != nil {
		return false
	}
	return a.Equal(b)
}
