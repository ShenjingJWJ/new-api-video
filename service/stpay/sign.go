package stpay

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// SignRequest hashes the documented create-order canonical string.
// The secret is appended after the last '&' and is not sent as a field.
func SignRequest(characterSet, merNo, terNo, orderNo, currencyCode, amount, payIP, transType, transModel, secret string) string {
	plain := "EncryptionMode=SHA256" +
		"&CharacterSet=" + characterSet +
		"&merNo=" + merNo +
		"&terNo=" + terNo +
		"&orderNo=" + orderNo +
		"&currencyCode=" + currencyCode +
		"&amount=" + amount +
		"&payIP=" + payIP +
		"&transType=" + transType +
		"&transModel=" + transModel +
		"&" + secret
	return sha256Hex(plain)
}

// SignResponse hashes the documented create/notify response canonical string.
func SignResponse(amount, currencyCode, merNo, orderNo, respCode, respMsg, terNo, tradeNo, transType, secret string) string {
	plain := "amount=" + amount +
		"&currencyCode=" + currencyCode +
		"&merNo=" + merNo +
		"&orderNo=" + orderNo +
		"&respCode=" + respCode +
		"&respMsg=" + respMsg +
		"&terNo=" + terNo +
		"&tradeNo=" + tradeNo +
		"&transType=" + transType +
		"&" + secret
	return sha256Hex(plain)
}

// SignQuery hashes concatenated query values, without field names.
func SignQuery(merNo, terNo, orderNo, amount, currency, secret string) string {
	return sha256Hex(merNo + terNo + orderNo + amount + currency + secret)
}

// SignQueryResponse hashes a successful query response.
// The provider returns the submitted signature unchanged when the query itself fails.
func SignQueryResponse(merNo, terNo, orderNo, amount, currency, tradeNo, respCode, secret string) string {
	return sha256Hex(merNo + terNo + orderNo + amount + currency + tradeNo + respCode + secret)
}

func sha256Hex(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// VerifyHex 以常量时间比较两个十六进制签名。
// 使用 subtle.ConstantTimeCompare 而不是 strings.EqualFold：后者短路比较，
// 理论上会通过响应时间按字节泄漏期望签名。
func VerifyHex(expected, actual string) bool {
	a := strings.ToLower(strings.TrimSpace(expected))
	b := strings.ToLower(strings.TrimSpace(actual))
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
