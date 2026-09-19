// Package credential 实现"签名的本地模拟凭证"。
//
// 凭证是离线借出后员工本地保存的归还票据,形如:
//
//	LIC1.<base64url(payload)>.<base64url(HMAC-SHA256(payload))>
//
// 服务端集中持有 HMAC 密钥,员工无法伪造;payload 内含借用ID与一次性
// return_nonce。归还时必须同时满足:签名正确、未到期、借用仍 ACTIVE,
// 三者缺一不可 —— 过期或重复使用的凭证都会被拒绝,席位只能由回收流程释放。
package credential

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const prefix = "LIC1"

var (
	ErrMalformed    = errors.New("malformed credential")
	ErrBadSignature = errors.New("credential signature mismatch")
	ErrExpired      = errors.New("credential expired")
)

type Claims struct {
	CheckoutID int64  `json:"cid"`
	PoolID     int64  `json:"pid"`
	Employee   string `json:"emp"`
	Mode       string `json:"mode"`
	Nonce      string `json:"nonce"`
	IssuedAt   int64  `json:"iat"`
	ExpiresAt  int64  `json:"exp"`
}

var b64 = base64.RawURLEncoding

func sign(macInput []byte, secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(macInput)
	return m.Sum(nil)
}

// Issue 生成签名凭证。
func Issue(c Claims, secret string) string {
	body, _ := json.Marshal(c)
	enc := b64.EncodeToString(body)
	sig := b64.EncodeToString(sign([]byte(prefix+"."+enc), []byte(secret)))
	return prefix + "." + enc + "." + sig
}

// Parse 校验格式与签名,返回 Claims。不判断业务状态。
func Parse(token, secret string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != prefix {
		return nil, ErrMalformed
	}
	want := b64.EncodeToString(sign([]byte(parts[0]+"."+parts[1]), []byte(secret)))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[2])) != 1 {
		return nil, ErrBadSignature
	}
	raw, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, ErrMalformed
	}
	return &c, nil
}

// VerifyExpiry 检查凭证是否在有效期内(now 之前到期即失效)。
func VerifyExpiry(c *Claims, now time.Time) error {
	if now.Unix() >= c.ExpiresAt {
		return fmt.Errorf("%w at %s", ErrExpired, time.Unix(c.ExpiresAt, 0).Format(time.RFC3339))
	}
	return nil
}

// ParseID 工具:字符串 ID 转 int64。
func ParseID(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
