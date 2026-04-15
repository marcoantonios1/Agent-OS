// gmailauth is a one-time helper that walks you through the Gmail OAuth2 flow
// and prints the refresh token you need to set as GMAIL_REFRESH_TOKEN.
//
// Usage:
//
//	GMAIL_CLIENT_ID=<id> GMAIL_CLIENT_SECRET=<secret> go run ./cmd/gmailauth/
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	googlemail "google.golang.org/api/gmail/v1"
)

func main() {
	clientID := os.Getenv("GMAIL_CLIENT_ID")
	clientSecret := os.Getenv("GMAIL_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("Set GMAIL_CLIENT_ID and GMAIL_CLIENT_SECRET before running this tool.")
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			googlemail.GmailReadonlyScope,
			googlemail.GmailComposeScope,
		},
		Endpoint:    google.Endpoint,
		RedirectURL: "urn:ietf:wg:oauth:2.0:oob", // copy-paste flow, no local server needed
	}

	url := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Gmail OAuth2 Setup")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("1. Open this URL in your browser:")
	fmt.Println()
	fmt.Println("  ", url)
	fmt.Println()
	fmt.Println("2. Sign in with your Google account and click Allow.")
	fmt.Println("3. Copy the authorisation code shown and paste it below.")
	fmt.Println()
	fmt.Print("Authorisation code: ")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	code := strings.TrimSpace(scanner.Text())
	if code == "" {
		log.Fatal("No code provided.")
	}

	token, err := cfg.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("Token exchange failed: %v", err)
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Success! Add these to your .env file:")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Printf("GMAIL_CLIENT_ID=%s\n", clientID)
	fmt.Printf("GMAIL_CLIENT_SECRET=%s\n", clientSecret)
	fmt.Printf("GMAIL_REFRESH_TOKEN=%s\n", token.RefreshToken)
	fmt.Println()
	fmt.Println("Keep the refresh token secret — it grants read/draft access to your Gmail.")
}
