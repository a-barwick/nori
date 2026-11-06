// Command nori-connect-calendar saves a Google calendar connection in the selected user row.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"golang.org/x/oauth2"
	"nori/internal/agent"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/users"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	userID := flag.String("user", "", "user ID to connect")
	flag.Parse()
	if !users.ValidID(*userID) {
		return fmt.Errorf("--user is required")
	}
	s, err := settings.Load()
	if err != nil {
		return err
	}
	file := s.EnvFile
	id, secret := s.Calendar.ClientID, s.Calendar.ClientSecret
	if id == "" || secret == "" {
		return fmt.Errorf("GOOGLE_CALENDAR_CLIENT_ID and GOOGLE_CALENDAR_CLIENT_SECRET must be set in %s", file)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	directory, err := platform.OpenUsers(ctx, s)
	if err != nil {
		return err
	}
	defer directory.Close()
	row, err := directory.Get(ctx, *userID)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	config := agent.CalendarOAuth(id, secret, "http://"+listener.Addr().String()+"/callback")
	state, verifier := randomText(), oauth2.GenerateVerifier()
	codes, failures := make(chan string, 1), make(chan error, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("state") != state {
			http.Error(w, "This sign-in link doesn't match the running command.", http.StatusBadRequest)
			return
		}
		if problem := query.Get("error"); problem != "" || query.Get("code") == "" {
			http.Error(w, "Google Calendar was not connected.", http.StatusBadRequest)
			failures <- fmt.Errorf("google did not grant access: %s", problem)
			return
		}
		fmt.Fprintln(w, "Google Calendar connected. You can close this tab.")
		codes <- query.Get("code")
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	fmt.Println("Open this URL and approve calendar and email access:")
	fmt.Println(config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier)))
	var code string
	select {
	case code = <-codes:
	case err = <-failures:
		return err
	case <-ctx.Done():
		return errors.New("no approval arrived before the 10-minute limit")
	}
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return errors.New("google refused the sign-in code")
	}
	if token.RefreshToken == "" {
		return errors.New("google returned no refresh token; remove Nori's access in your Google account and run this again")
	}
	account, err := primaryAccount(ctx, config.Client(ctx, token))
	if err != nil {
		return err
	}
	if err = directory.SetCalendar(ctx, row.ID, users.Calendar{RefreshToken: token.RefreshToken, Account: account, WriteID: row.Calendar.WriteID}); err != nil {
		return err
	}
	fmt.Printf("Connected calendar for user %s.\n", row.ID)
	return nil
}

func randomText() string {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}

// A primary calendar's ID is its account's email address, so no profile scope is needed.
func primaryAccount(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/calendar/v3/calendars/primary", nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("the connected calendar could not be read")
	}
	defer res.Body.Close()
	var calendar struct {
		ID string `json:"id"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&calendar) != nil || calendar.ID == "" {
		return "", fmt.Errorf("the connected calendar returned HTTP %d", res.StatusCode)
	}
	return calendar.ID, nil
}
