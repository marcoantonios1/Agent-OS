// googleauth is a one-time helper that walks you through the Google OAuth2 flow
// and prints the single refresh token that covers both Gmail and Google Calendar.
//
// It starts a temporary localhost callback server so you never need to
// copy-paste an authorisation code.
//
// Usage:
//
//	GOOGLE_CLIENT_ID=<id> GOOGLE_CLIENT_SECRET=<secret> go run ./cmd/tool/googleauth/
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	googlecal "google.golang.org/api/calendar/v3"
	googlemail "google.golang.org/api/gmail/v1"
)

func main() {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("Set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET before running this tool.")
	}

	// Pick a free port for the local callback server.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Could not bind a local port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			googlemail.GmailReadonlyScope,
			googlemail.GmailComposeScope,
			googlecal.CalendarReadonlyScope,
			googlecal.CalendarEventsScope,
		},
		Endpoint:    google.Endpoint,
		RedirectURL: redirectURL,
	}

	authURL := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Google OAuth2 Setup (Gmail + Calendar)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("1. Open this URL in your browser:")
	fmt.Println()
	fmt.Println("  ", authURL)
	fmt.Println()
	fmt.Println("2. Sign in with your Google account and click Allow.")
	fmt.Println("   The token will be captured automatically — no copy-pasting needed.")
	fmt.Println()
	fmt.Printf("Waiting for callback on http://127.0.0.1:%d ...\n", port)

	// codeCh receives the authorisation code from the callback handler.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errMsg := r.URL.Query().Get("error")
			fmt.Fprintf(w, "<html><body><h2>Error: %s</h2><p>You can close this tab.</p></body></html>", errMsg)
			errCh <- fmt.Errorf("auth error: %s", errMsg)
			return
		}
		fmt.Fprint(w, "<html><body><h2>Authorised!</h2><p>You can close this tab and return to the terminal.</p></body></html>")
		codeCh <- code
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("local server error: %w", err)
		}
	}()

	// Block until the callback arrives or an error occurs.
	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		log.Fatalf("Failed: %v", err)
	}
	srv.Close()

	token, err := cfg.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("Token exchange failed: %v", err)
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Success! Add these to your .env file:")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Printf("GOOGLE_CLIENT_ID=%s\n", clientID)
	fmt.Printf("GOOGLE_CLIENT_SECRET=%s\n", clientSecret)
	fmt.Printf("GOOGLE_REFRESH_TOKEN=%s\n", token.RefreshToken)
	fmt.Println()
	fmt.Println("This single token grants access to both Gmail and Google Calendar.")
	fmt.Println("Keep the refresh token secret.")
}
