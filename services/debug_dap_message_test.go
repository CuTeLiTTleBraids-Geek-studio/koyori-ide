package services

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// TestReadDAPMessage_RejectsOversized verifies that a Content-Length
// exceeding the 16 MB limit is rejected with an error, preventing OOM
// from a malformed or malicious header (M-9).
func TestReadDAPMessage_RejectsOversized(t *testing.T) {
	// 16 MB + 1 byte — just over the limit.
	oversized := maxDAPContentLength + 1
	raw := "Content-Length: " + itoa(oversized) + "\r\n\r\n"
	r := bufio.NewReader(strings.NewReader(raw))
	_, err := readDAPMessage(r)
	if err == nil {
		t.Fatal("expected error for oversized Content-Length, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("expected 'exceeds maximum' error, got: %v", err)
	}
}

// TestReadDAPMessage_RejectsExactlyAtLimit verifies that a Content-Length
// exactly at the limit is accepted (boundary check). We don't actually
// send a body this large — we just verify the error is NOT "exceeds
// maximum". The read will fail with an io.ErrUnexpectedEOF since the
// body is missing, which is fine.
func TestReadDAPMessage_RejectsExactlyAtLimit(t *testing.T) {
	raw := "Content-Length: " + itoa(maxDAPContentLength) + "\r\n\r\n"
	r := bufio.NewReader(strings.NewReader(raw))
	_, err := readDAPMessage(r)
	if err != nil && strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("Content-Length at limit should not be rejected as oversized: %v", err)
	}
	// An io error (unexpected EOF) is expected since we didn't send the body.
}

// TestReadDAPMessage_RobustParsing verifies that Content-Length is parsed
// correctly across various header formats (M-9).
func TestReadDAPMessage_RobustParsing(t *testing.T) {
	body := `{"seq":1,"type":"request","command":"test"}`
	cases := []struct {
		name   string
		header string
	}{
		{"standard", "Content-Length: " + itoa(len(body)) + "\r\n"},
		{"lowercase", "content-length: " + itoa(len(body)) + "\r\n"},
		{"uppercase", "CONTENT-LENGTH: " + itoa(len(body)) + "\r\n"},
		{"mixed-case", "CoNtEnT-LeNgTh: " + itoa(len(body)) + "\r\n"},
		{"no-space-after-colon", "Content-Length:" + itoa(len(body)) + "\r\n"},
		{"extra-spaces", "Content-Length:   " + itoa(len(body)) + "   \r\n"},
		{"with-trailing-tab", "Content-Length: " + itoa(len(body)) + "\t\r\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := c.header + "\r\n" + body
			r := bufio.NewReader(strings.NewReader(raw))
			msg, err := readDAPMessage(r)
			if err != nil {
				t.Fatalf("readDAPMessage failed for %s: %v", c.name, err)
			}
			if msg.Type != "request" || msg.Command != "test" || msg.Seq != 1 {
				t.Errorf("%s: parsed incorrectly: %+v", c.name, msg)
			}
		})
	}
}

// TestReadDAPMessage_InvalidContentLength verifies that a non-numeric
// Content-Length value is rejected with an error (M-9: don't ignore
// strconv.Atoi errors).
func TestReadDAPMessage_InvalidContentLength(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"non-numeric", "Content-Length: abc\r\n"},
		{"empty-value", "Content-Length:\r\n"},
		{"negative", "Content-Length: -1\r\n"},
		{"float", "Content-Length: 3.5\r\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := c.header + "\r\n"
			r := bufio.NewReader(strings.NewReader(raw))
			_, err := readDAPMessage(r)
			if err == nil {
				t.Fatalf("expected error for %s Content-Length, got nil", c.name)
			}
		})
	}
}

// TestReadDAPMessage_MissingContentLength verifies that a message
// without any Content-Length header is rejected.
func TestReadDAPMessage_MissingContentLength(t *testing.T) {
	raw := "Some-Other-Header: value\r\n\r\n"
	r := bufio.NewReader(strings.NewReader(raw))
	_, err := readDAPMessage(r)
	if err == nil {
		t.Fatal("expected error for missing Content-Length, got nil")
	}
	if !strings.Contains(err.Error(), "content-length") {
		t.Errorf("expected content-length error, got: %v", err)
	}
}

// TestReadDAPMessage_ZeroContentLength verifies that Content-Length: 0
// is rejected (no valid DAP message has an empty body).
func TestReadDAPMessage_ZeroContentLength(t *testing.T) {
	raw := "Content-Length: 0\r\n\r\n"
	r := bufio.NewReader(strings.NewReader(raw))
	_, err := readDAPMessage(r)
	if err == nil {
		t.Fatal("expected error for Content-Length: 0, got nil")
	}
}

// itoa is a local helper to avoid importing strconv in the test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf bytes.Buffer
	for n > 0 {
		buf.WriteByte(byte('0' + n%10))
		n /= 10
	}
	if neg {
		buf.WriteByte('-')
	}
	// Reverse
	b := buf.Bytes()
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// TestDebugService_M9_ContentLengthParsing 验证 M-9 鲁棒的
// Content-Length 解析,覆盖四个子场景:
// (a) 合法 Content-Length 正确解析;
// (b) 缺失 Content-Length 返回错误;
// (c) Content-Length 超过 16MB 上限返回错误;
// (d) 非数字 Content-Length 返回错误(而非被静默当作 0)。
func TestDebugService_M9_ContentLengthParsing(t *testing.T) {
	// (a) 合法 Content-Length 正确解析。
	t.Run("valid_content_length_parses", func(t *testing.T) {
		body := `{"seq":1,"type":"request","command":"test"}`
		raw := "Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body
		r := bufio.NewReader(strings.NewReader(raw))
		msg, err := readDAPMessage(r)
		if err != nil {
			t.Fatalf("readDAPMessage failed for valid header: %v", err)
		}
		if msg.Type != "request" || msg.Command != "test" || msg.Seq != 1 {
			t.Errorf("parsed incorrectly: %+v", msg)
		}
	})

	// (b) 缺失 Content-Length 返回错误。
	t.Run("missing_content_length_returns_error", func(t *testing.T) {
		raw := "Some-Other-Header: value\r\n\r\n"
		r := bufio.NewReader(strings.NewReader(raw))
		_, err := readDAPMessage(r)
		if err == nil {
			t.Fatal("expected error for missing Content-Length, got nil")
		}
		if !strings.Contains(err.Error(), "content-length") {
			t.Errorf("expected content-length error, got: %v", err)
		}
	})

	// (c) Content-Length > 16MB 返回错误。
	t.Run("oversized_content_length_returns_error", func(t *testing.T) {
		oversized := maxDAPContentLength + 1
		raw := "Content-Length: " + itoa(oversized) + "\r\n\r\n"
		r := bufio.NewReader(strings.NewReader(raw))
		_, err := readDAPMessage(r)
		if err == nil {
			t.Fatal("expected error for oversized Content-Length, got nil")
		}
		if !strings.Contains(err.Error(), "exceeds maximum") {
			t.Errorf("expected 'exceeds maximum' error, got: %v", err)
		}
	})

	// (d) 非数字 Content-Length 返回错误,而非被静默当作 0。
	t.Run("non_numeric_content_length_returns_error", func(t *testing.T) {
		raw := "Content-Length: abc\r\n\r\n"
		r := bufio.NewReader(strings.NewReader(raw))
		_, err := readDAPMessage(r)
		if err == nil {
			t.Fatal("expected error for non-numeric Content-Length, got nil")
		}
		// 确认不是 "missing or invalid content-length"(被当作 0 的路径),
		// 而是显式的 Atoi 解析错误。
		if !strings.Contains(err.Error(), "invalid Content-Length") {
			t.Errorf("expected 'invalid Content-Length' parse error, got: %v", err)
		}
	})
}
