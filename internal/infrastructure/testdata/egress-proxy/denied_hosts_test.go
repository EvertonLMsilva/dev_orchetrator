package main

import "testing"

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
