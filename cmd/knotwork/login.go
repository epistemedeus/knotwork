package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type deviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type deviceToken struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func runAuth(args []string) int {
	if len(args) == 0 || args[0] != "login" {
		fmt.Fprintln(os.Stderr, "usage: knotwork auth login [--server URL] [--no-browser] [--print-token]")
		return 2
	}
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	server := fs.String("server", "http://localhost:8080", "Knotwork base URL")
	noBrowser := fs.Bool("no-browser", false, "do not open a browser")
	printToken := fs.Bool("print-token", false, "print the token instead of relying on git's credential helper")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	base := strings.TrimRight(*server, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fmt.Fprintln(os.Stderr, "invalid --server URL")
		return 2
	}

	client := &http.Client{Timeout: 20 * time.Second}
	code, err := requestDeviceCode(client, base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "knotwork:", err)
		return 1
	}
	fmt.Println("Open this URL in a browser and confirm the code:")
	fmt.Printf("  %s\n", code.VerificationURIComplete)
	fmt.Printf("  Code: %s\n", code.UserCode)
	if !*noBrowser {
		openBrowser(code.VerificationURIComplete)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(code.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "device authorization expired")
			return 1
		case <-time.After(interval):
		}
		token, pending, err := pollDeviceToken(ctx, client, base, code.DeviceCode)
		if err != nil {
			fmt.Fprintln(os.Stderr, "knotwork:", err)
			return 1
		}
		if pending == "slow_down" {
			interval += 5 * time.Second
			continue
		}
		if pending != "" {
			continue
		}
		fmt.Println("Authorized.")
		if *printToken {
			fmt.Println(token)
			return 0
		}
		if err := storeGitCredential(base, token); err != nil {
			fmt.Fprintln(os.Stderr, "could not store git credential:", err)
			fmt.Println(token)
			return 1
		}
		fmt.Printf("Git credential stored for %s. Try `git clone %s/<owner>/<repo>.git`.\n", u.Host, base)
		return 0
	}
}

func requestDeviceCode(client *http.Client, base string) (deviceCode, error) {
	var out deviceCode
	req, err := http.NewRequest(http.MethodPost, base+"/auth/device/code", nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("device code request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// pollDeviceToken returns pending="authorization_pending" or "slow_down" while
// the user has not approved; pending is empty on success.
func pollDeviceToken(ctx context.Context, client *http.Client, base, code string) (token string, pending string, err error) {
	form := url.Values{"device_code": {code}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/auth/device/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out deviceToken
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	if resp.StatusCode == http.StatusOK && out.AccessToken != "" {
		return out.AccessToken, "", nil
	}
	switch out.Error {
	case "authorization_pending", "slow_down":
		return "", out.Error, nil
	case "":
		return "", "", fmt.Errorf("authorization failed: %s", resp.Status)
	default:
		return "", "", fmt.Errorf("%s: %s", out.Error, out.ErrorDescription)
	}
}

func storeGitCredential(base, token string) error {
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	payload := fmt.Sprintf("protocol=%s\nhost=%s\nusername=knotwork\npassword=%s\n\n", u.Scheme, u.Host, token)
	cmd := exec.Command("git", "credential", "approve")
	cmd.Stdin = bytes.NewBufferString(payload)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
