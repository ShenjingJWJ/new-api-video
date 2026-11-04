package stpay

import "strings"

// NormalizeCardType 归一化渠道支付方式：空值回落到生产值 sbp。
//
// 控制器与渠道客户端共用这一个实现，避免「两处各写一遍默认值」这类走样。
// 正式环境必须为 sbp（俄罗斯 SBP 二维码）；测试 MID(1888/88816) 未开通 SBP 通道，
// 需显式配成 online_banking。
func NormalizeCardType(cardType string) string {
	if v := strings.TrimSpace(cardType); v != "" {
		return v
	}
	return CardTypeSBP
}

// Configured 表示渠道参数是否齐备到可以发起请求。
// 只做非空判断；URL 合法性由 joinURL 在真正请求时校验并回报。
func (c *Client) Configured() bool {
	if c == nil {
		return false
	}
	return strings.TrimSpace(c.BaseURL) != "" &&
		strings.TrimSpace(c.MerNo) != "" &&
		strings.TrimSpace(c.TerNo) != "" &&
		strings.TrimSpace(c.HashSecret) != ""
}
