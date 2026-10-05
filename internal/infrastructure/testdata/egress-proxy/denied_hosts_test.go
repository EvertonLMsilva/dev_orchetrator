package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSafeConnectionOutcomes(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`CONNECT   Oct 05 12:30:00.123 [7]: Established connection to host "chatgpt.com" using file descriptor 5.`, "CONNECT_STATE chatgpt.com 443 SUCCESS SUCCESS UNKNOWN"},
		{`ERROR     Oct 05 12:30:00.123 [7]: opensock: Could not establish a connection to chatgpt.com:443`, "CONNECT_STATE chatgpt.com 443 SUCCESS FAIL NO"},
		{`ERROR     Oct 05 12:30:00.123 [7]: opensock: Could not retrieve address info for chatgpt.com:443: SECRET`, "CONNECT_STATE chatgpt.com 443 FAIL UNKNOWN NO"},
		{`ERROR     Oct 05 12:30:00.123 [7]: Authorization: Bearer SECRET`, ""},
		{`CONNECT   Oct 05 12:30:00.123 [7]: Established connection to host "user:SECRET@chatgpt.com" using file descriptor 5.`, ""},
		{`ERROR     Oct 05 12:30:00.123 [7]: auth.json access_token refresh_token id_token account_id SECRET`, ""},
	} {
		if got := connectionDiagnostic(tc.line); got != tc.want {
			t.Fatal("unsafe or incorrect connection outcome")
		}
	}
}

func TestDiagnosticOutputBounded(t *testing.T) {
	line := "CONNECT   Oct 05 12:30:00.123 [7]: Request (file descriptor 4): CONNECT chatgpt.com:443 HTTP/1.1\n"
	var output bytes.Buffer
	sanitizeProxyLogs(strings.NewReader(strings.Repeat(line, 300)+"Authorization: SECRET\nHTTPS_PAYLOAD_SECRET\n"), &output)
	if strings.Count(output.String(), "\n") != 128 || strings.Contains(output.String(), "SECRET") {
		t.Fatal("diagnostics not bounded or sanitized")
	}
}

func TestConnectDiagnosticOnly(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`CONNECT   Oct 05 12:30:00.123 [7]: Request (file descriptor 4): CONNECT chatgpt.com:443 HTTP/1.1`, "CONNECT_HOST chatgpt.com 443"},
		{`CONNECT   Oct 05 12:30:00.123 [7]: Request (file descriptor 4): CONNECT unknown.example:80 HTTP/1.1`, "CONNECT_HOST unknown.example 80"},
		{`CONNECT   Oct 05 12:30:00.123 [7]: Request (file descriptor 4): CONNECT user:TOKEN_SECRET@chatgpt.com:443 HTTP/1.1`, ""},
		{`CONNECT   Oct 05 12:30:00.123 [7]: Request (file descriptor 4): GET https://chatgpt.com/?token=TOKEN_SECRET HTTP/1.1`, ""},
		{`CONNECT   Oct 05 12:30:00.123 [7]: Request (file descriptor 4): CONNECT chatgpt.com:443/path HTTP/1.1`, ""},
	} {
		if got := connectDiagnostic(tc.line); got != tc.want {
			t.Fatal("CONNECT sanitization mismatch")
		}
	}
}

func TestDeniedHostOnly(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`NOTICE    Oct 05 12:30:00.123 [7]: Proxying refused on filtered url "unknown.example:443"`, "unknown.example"},
		{`NOTICE    Oct 05 12:30:00.123 [7]: Proxying refused on filtered url "http://unknown.example/?token=TEST_SECRET_DO_NOT_LEAK"`, ""},
		{`CONNECT   Oct 05 12:30:00.123 [7]: Request: TEST_SECRET_DO_NOT_LEAK`, ""},
		{`NOTICE    Oct 05 12:30:00.123 [7]: Proxying refused on filtered url "user:TEST_SECRET_DO_NOT_LEAK@unknown.example:443"`, ""},
		{`NOTICE    Oct 05 12:30:00.123 [7]: Proxying refused on filtered url "unknown.example:443/path"`, ""},
		{`NOTICE    Oct 05 12:30:00.123 [7]: Proxying refused on filtered url "127.0.0.1:443"`, ""},
		{`NOTICE    Oct 05 12:30:00.123 [7]: Proxying refused on filtered url "unknown.example:80"`, ""},
	} {
		if deniedHost(tc.line) != tc.want {
			t.Fatal("hostname redaction mismatch")
		}
	}
}
