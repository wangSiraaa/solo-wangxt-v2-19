package credential

import (
	"strings"
	"testing"
	"time"
)

func sampleClaims() Claims {
	return Claims{
		Token:     "tok-1",
		SeatID:    7,
		DeptID:    3,
		User:      "alice",
		Offline:   true,
		IssuedAt:  time.Unix(1000, 0),
		ExpiresAt: time.Unix(2000, 0),
		RequestID: "rid-1",
	}
}

func TestIssueParseRoundtrip(t *testing.T) {
	s := NewSigner("secret-key")
	tok, err := s.Issue(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(tok, ".") != 1 {
		t.Fatalf("凭证应为 payload.sig 两段，实际 %q", tok)
	}
	got, err := s.Parse(tok)
	if err != nil {
		t.Fatalf("合法凭证解析失败: %v", err)
	}
	want := sampleClaims()
	if got.Token != want.Token || got.SeatID != want.SeatID || got.DeptID != want.DeptID ||
		got.User != want.User || got.Offline != want.Offline || got.RequestID != want.RequestID ||
		!got.IssuedAt.Equal(want.IssuedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("claims 往返不一致: %+v", got)
	}
}

func TestRejectTamperedAndForged(t *testing.T) {
	s := NewSigner("secret-key")
	tok, _ := s.Issue(sampleClaims())

	parts := strings.SplitN(tok, ".", 2)
	// 篡改 payload
	if _, err := s.Parse("AAAA" + parts[0][4:] + "." + parts[1]); err != ErrSignature {
		t.Fatalf("篡改 payload 应报签名错误，got %v", err)
	}
	// 篡改签名
	tampered := tok[:len(tok)-1]
	if last := tok[len(tok)-1]; last == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}
	if _, err := s.Parse(tampered); err != ErrSignature {
		t.Fatalf("篡改签名应报签名错误，got %v", err)
	}
	// 另一把密钥签发的凭证
	other, _ := NewSigner("attacker-key").Issue(sampleClaims())
	if _, err := s.Parse(other); err != ErrSignature {
		t.Fatalf("异密钥凭证必须被拒绝，got %v", err)
	}
	// 格式错误
	for _, bad := range []string{"", "xxx", "a.b.c", ".", "a.b"} {
		if _, err := s.Parse(bad); err == nil {
			t.Fatalf("非法凭证 %q 应解析失败", bad)
		}
	}
}
