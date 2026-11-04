package middleware

import (
	"github.com/gin-gonic/gin"
)

// PaymentGatewayRateLimit limits one gateway's quote and checkout requests per
// user. The limits are read on every request so an administrator's change
// takes effect without a restart; a count of 0 disables the limit.
func PaymentGatewayRateLimit(limit, duration *int, mark string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if *limit <= 0 {
			c.Next()
			return
		}
		window := *duration
		if window <= 0 {
			window = 60
		}
		userRateLimitFactory(*limit, int64(window), mark)(c)
	}
}
