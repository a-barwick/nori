// Command nori-connect-calendar connects Austin's Google account for calendar and
// email tools. It opens one Google consent screen, then saves the refresh token and
// account name in the existing .env and in Secret Manager, so the deployed worker gets
// the same connection. It never prints the token.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"nori/internal/agent"
	"nori/internal/env"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if err := env.Load(); err != nil {
		return err
	}
	file := env.Get("NORI_ENV_FILE", "../.env")
	id, secret := os.Getenv("GOOGLE_CALENDAR_CLIENT_ID"), os.Getenv("GOOGLE_CALENDAR_CLIENT_SECRET")
	if id == "" || secret == "" {
		return fmt.Errorf("GOOGLE_CALENDAR_CLIENT_ID and GOOGLE_CALENDAR_CLIENT_SECRET must be set in %s", file)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
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
	if err = updateEnv(file, map[string]string{"GOOGLE_CALENDAR_REFRESH_TOKEN": token.RefreshToken, "GOOGLE_CALENDAR_ACCOUNT": account}); err != nil {
		return err
	}
	fmt.Printf("Connected %s. Saved in %s.\n", account, file)
	return saveSecrets(ctx, map[string]string{"nori-google-calendar-refresh-token": token.RefreshToken, "nori-google-calendar-account": account})
}

// saveSecrets adds new versions with the caller's gcloud login. The value goes over
// stdin, so it never appears in a process listing. A running worker keeps the old
// version until its next start.
func saveSecrets(ctx context.Context, values map[string]string) error {
	project := env.Get("NORI_PROJECT_ID", "bot-111549")
	for name, value := range values {
		command := exec.CommandContext(ctx, "gcloud", "secrets", "versions", "add", name, "--project", project, "--data-file=-")
		command.Stdin = strings.NewReader(value)
		if err := command.Run(); err != nil {
			return fmt.Errorf("%s was not updated in Secret Manager, so production still has the old connection: %w", name, err)
		}
		fmt.Printf("Added a new version of %s.\n", name)
	}
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

// updateEnv replaces only the named lines and keeps the file at mode 600. It writes
// through a symlink to its target, because core/.env links to the root file.
func updateEnv(file string, values map[string]string) error {
	path, err := filepath.EvalSymlinks(file)
	if err != nil {
		return err
	}
	current, err := os.Open(path)
	if err != nil {
		return err
	}
	defer current.Close()
	lines := []string{}
	scanner := bufio.NewScanner(current)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		name, _, _ := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if value, ok := values[strings.TrimSpace(name)]; ok {
			line = strings.TrimSpace(name) + "=" + value
			delete(values, strings.TrimSpace(name))
		}
		lines = append(lines, line)
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	for _, name := range []string{"GOOGLE_CALENDAR_REFRESH_TOKEN", "GOOGLE_CALENDAR_ACCOUNT"} {
		if value, ok := values[name]; ok {
			lines = append(lines, name+"="+value)
		}
	}
	next, err := os.CreateTemp(filepath.Dir(path), ".env.next-*")
	if err != nil {
		return err
	}
	defer os.Remove(next.Name())
	if err = next.Chmod(0600); err == nil {
		_, err = next.WriteString(strings.Join(lines, "\n") + "\n")
	}
	if err = errors.Join(err, next.Close()); err != nil {
		return err
	}
	return os.Rename(next.Name(), path)
}
