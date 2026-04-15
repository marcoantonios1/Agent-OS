// outlookcalauth is a one-time helper that uses the OAuth2 device code flow to
// obtain a refresh token for Outlook Calendar access.
//
// No redirect URIs, no local server, no copy-pasting codes from the URL bar.
// You get a short human-readable code, visit a Microsoft URL, sign in, and
// the refresh token is printed automatically.
//
// Usage:
//
//	OUTLOOK_CAL_CLIENT_ID=<id> go run ./cmd/outlookcalauth/
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"golang.org/x/oauth2"
)

var microsoftEndpoint = oauth2.Endpoint{
	AuthURL:       "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
	TokenURL:      "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
	DeviceAuthURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode",
}

func main() {
	clientID := os.Getenv("OUTLOOK_CAL_CLIENT_ID")
	if clientID == "" {
		log.Fatal("Set OUTLOOK_CAL_CLIENT_ID before running this tool.")
	}

	cfg := &oauth2.Config{
		ClientID: clientID,
		Scopes:   []string{"offline_access", "Calendars.Read", "Calendars.ReadWrite"},
		Endpoint: microsoftEndpoint,
	}

	ctx := context.Background()

	// Step 1: request a device code.
	deviceAuth, err := cfg.DeviceAuth(ctx)
	if err != nil {
		log.Fatalf("Device auth request failed: %v", err)
	}

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Outlook Calendar OAuth2 Setup — Device Code Flow")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("1. Open this URL in your browser:")
	fmt.Println()
	fmt.Println("  ", deviceAuth.VerificationURI)
	fmt.Println()
	fmt.Println("2. Enter this code when prompted:")
	fmt.Println()
	fmt.Printf("     %s\n", deviceAuth.UserCode)
	fmt.Println()
	fmt.Println("3. Sign in with your Microsoft account and click Accept.")
	fmt.Println()
	fmt.Println("Waiting for you to complete sign-in (this will update automatically)...")

	// Step 2: poll until the user completes sign-in.
	token, err := cfg.DeviceAccessToken(ctx, deviceAuth)
	if err != nil {
		log.Fatalf("Failed to get token: %v", err)
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Success! Add these to your .env file:")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Printf("OUTLOOK_CAL_CLIENT_ID=%s\n", clientID)
	fmt.Printf("OUTLOOK_CAL_REFRESH_TOKEN=%s\n", token.RefreshToken)
	fmt.Println()
	fmt.Println("No client secret needed with device code flow.")
	fmt.Println("Keep the refresh token secret — it grants read/write access to your Outlook Calendar.")
}
