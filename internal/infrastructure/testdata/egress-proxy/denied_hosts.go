package main

import (
	"bufio"
	"fmt"
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
func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 8192)
	for scanner.Scan() {
		if diagnostic := connectDiagnostic(scanner.Text()); diagnostic != "" {
			fmt.Println(diagnostic)
		}
		if host := deniedHost(scanner.Text()); host != "" {
			fmt.Println("DENIED_HOST " + host)
		}
	}
}
