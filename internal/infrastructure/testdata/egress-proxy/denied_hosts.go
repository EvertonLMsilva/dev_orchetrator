package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
)

// Raw Tinyproxy diagnostics stay in an anonymous pipe. Only this exact pinned
// NOTICE shape, containing a bare DNS authority, reaches container logs.
var denied = regexp.MustCompile(`^NOTICE +[A-Z][a-z]{2} [0-9]{2} [0-9:.]+ \[[0-9]+\]: Proxying refused on filtered url "([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+):443"$`)
var numeric = regexp.MustCompile(`^[0-9.]+$`)

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
		if host := deniedHost(scanner.Text()); host != "" {
			fmt.Println("DENIED_HOST " + host)
		}
	}
}
