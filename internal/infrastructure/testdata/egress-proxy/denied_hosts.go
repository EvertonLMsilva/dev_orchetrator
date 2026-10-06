package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

// Raw Tinyproxy diagnostics stay in an anonymous pipe. Only validated bare
// DNS authorities and numeric CONNECT ports from pinned shapes reach logs.
var denied = regexp.MustCompile(`^NOTICE +[A-Z][a-z]{2} [0-9]{2} [0-9:.]+ \[[0-9]+\]: Proxying refused on filtered url "([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+):443"$`)
var numeric = regexp.MustCompile(`^[0-9.]+$`)
var connect = regexp.MustCompile(`^CONNECT +[A-Z][a-z]{2} [0-9]{2} [0-9:.]+ \[[0-9]+\]: Request \(file descriptor [0-9]+\): CONNECT ([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+):([0-9]{1,5}) HTTP/1\.[01]$`)

func connectDiagnostic(line string) string {
	m := connect.FindStringSubmatch(line)
	if len(m) != 3 || len(m[1]) > 253 || numeric.MatchString(m[1]) {
		return ""
	}
	port, err := strconv.Atoi(m[2])
	if err != nil || port < 1 || port > 65535 {
		return ""
	}
	return "CONNECT_HOST " + m[1] + " " + strconv.Itoa(port)
}

func deniedHost(line string) string {
	match := denied.FindStringSubmatch(line)
	if len(match) != 2 || len(match[1]) > 253 || numeric.MatchString(match[1]) {
		return ""
	}
	return match[1]
}

// Pinned 1.11.3 direct-connect messages. Never forward error text, addresses,
// headers or bodies. Successful TCP connection implies DNS succeeded, but
// does not prove CONNECT greeting delivery or TLS success.
var outcome = regexp.MustCompile(`^(CONNECT|ERROR) +[A-Z][a-z]{2} [0-9]{2} [0-9:.]+ \[[0-9]+\]: (.*)$`)
var established = regexp.MustCompile(`^Established connection to host "([a-z0-9.-]+)" using file descriptor [0-9]+\.$`)
var dnsFailed = regexp.MustCompile(`^opensock: Could not retrieve address info for ([a-z0-9.-]+):443: .*$`)
var tcpFailed = regexp.MustCompile(`^opensock: Could not establish a connection to ([a-z0-9.-]+):443$`)

func connectionDiagnostic(line string) string {
	m := outcome.FindStringSubmatch(line)
	if len(m) != 3 {
		return ""
	}
	var host, state string
	if v := established.FindStringSubmatch(m[2]); m[1] == "CONNECT" && len(v) == 2 {
		host, state = v[1], "SUCCESS SUCCESS UNKNOWN"
	}
	if v := dnsFailed.FindStringSubmatch(m[2]); m[1] == "ERROR" && len(v) == 2 {
		host, state = v[1], "FAIL UNKNOWN NO"
	}
	if v := tcpFailed.FindStringSubmatch(m[2]); m[1] == "ERROR" && len(v) == 2 {
		host, state = v[1], "SUCCESS FAIL NO"
	}
	// Reuse strict bare-host validation rather than emitting arbitrary captures.
	if state == "" || connectDiagnostic("CONNECT   Oct 05 12:30:00 [1]: Request (file descriptor 1): CONNECT "+host+":443 HTTP/1.1") == "" {
		return ""
	}
	return "CONNECT_STATE " + host + " 443 " + state
}

func sanitizeProxyLogs(input io.Reader, output io.Writer) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 8192)
	emitted := 0
	for scanner.Scan() {
		diagnostic := connectDiagnostic(scanner.Text())
		if diagnostic == "" {
			diagnostic = connectionDiagnostic(scanner.Text())
		}
		if host := deniedHost(scanner.Text()); host != "" {
			diagnostic = "DENIED_HOST " + host
		}
		if diagnostic != "" && emitted < 128 {
			fmt.Fprintln(output, diagnostic)
			emitted++
		}
	}
}
func main() { sanitizeProxyLogs(os.Stdin, os.Stdout) }
