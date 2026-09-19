// Package credential 实现"本地模拟凭证"：
// 借用成功后服务端签发一段带签名的不透明凭证（离线时由员工本地保存），
// 提前归还时必须出示该凭证；凭证过期后作废，席位只能由系统回收。
//
// 凭证格式：base64url(JSONPayload).base64url(HMAC_SHA256)
// 用 HMAC 模拟真实离线许可证方案中的非对称/厂商签名，仅作演示用途。
package credential

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims 是凭证中可被签名保护的内容。
type Claims struct {
	Token     string    `json:"jti"`           // 凭证唯一 ID，对应 checkouts.token
	SeatID    int64     `json:"seat"`          // 占用的席位 ID
	DeptID    int64     `json:"dept"`          // 所属部门
	User      string    `json:"user"`          // 借用人
	Offline   bool      `json:"offline"`       // 是否离线借出
	IssuedAt  time.Time `json:"iat"`           // 签发时间
	ExpiresAt time.Time `json:"exp"`           // 到期时间
	RequestID string    `json:"rid,omitempty"` // 申请幂等键
}

// Signer 负责签发与校验凭证。
type Signer struct {
	key []byte
}

func NewSigner(secret string) *Signer {
	return &Signer{key: []byte(secret)}
}

var (
	ErrMalformed = errors.New("凭证格式无效")
	ErrSignature = errors.New("凭证签名无效")
)

// Issue 生成签名凭证字符串。
func (s *Signer) Issue(c Claims) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

// Parse 校验签名并解析凭证。签名错误、格式错误一律拒绝。
func (s *Signer) Parse(token string) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, ErrMalformed
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return c, ErrSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return c, nil
}
