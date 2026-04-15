// googlecalauth is a one-time helper that walks you through the Google Calendar
// OAuth2 flow and prints the refresh token you need to set as
// GOOGLE_CAL_REFRESH_TOKEN.
//
// Usage:
//
//	GOOGLE_CAL_CLIENT_ID=<id> GOOGLE_CAL_CLIENT_SECRET=<secret> go run ./cmd/googlecalauth/
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
	googlecal "google.golang.org/api/calendar/v3"
)

func main() {
	clientID := os.Getenv("GOOGLE_CAL_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CAL_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("Set GOOGLE_CAL_CLIENT_ID and GOOGLE_CAL_CLIENT_SECRET before running this tool.")
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			googlecal.CalendarReadonlyScope,
			googlecal.CalendarEventsScope,
		},
		Endpoint:    google.Endpoint,
		RedirectURL: "urn:ietf:wg:oauth:2.0:oob", // copy-paste flow, no local server needed
	}

	url := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Google Calendar OAuth2 Setup")
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
	fmt.Printf("GOOGLE_CAL_CLIENT_ID=%s\n", clientID)
	fmt.Printf("GOOGLE_CAL_CLIENT_SECRET=%s\n", clientSecret)
	fmt.Printf("GOOGLE_CAL_REFRESH_TOKEN=%s\n", token.RefreshToken)
	fmt.Println()
	fmt.Println("Keep the refresh token secret — it grants read/write access to your Google Calendar.")
}
