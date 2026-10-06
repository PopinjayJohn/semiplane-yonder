package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// promptPassword reads one line from stdin. stdlib-only (no x/term in the
// frozen dep set), so input echoes: warn the operator and prefer the
// --gm-password flag or YONDER_GM_PASSWORD env in shared spaces.
func promptPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt+" (input echoes; prefer --gm-password or YONDER_GM_PASSWORD) ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	return pw, nil
}
