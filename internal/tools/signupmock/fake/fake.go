// Package fake is an in-memory stand-in for everything 'bronto signup'
// talks to: the Cognito user pool (SignUp, ConfirmSignUp and the SRP
// sign-in pair), the slice of the Bronto API a brand-new account uses
// (POST /api-keys, GET /logs, ingestion), and an "inbox" that returns the
// verification email. PROTOTYPE: it backs the signup test and the
// recordings in docs/tapes/signup. It does not verify the SRP proof.
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
)

const (
	// APIKey is the key POST /api-keys hands out, and the only one accepted.
	APIKey = "bronto_fake_signup_key_0000000000" // #nosec G101 -- fake
	// Code is the confirmation code in every verification email.
	Code    = "424242"
	idToken = "fake.id.token"
)

type user struct {
	confirmed bool
	first     string
}

// Server is the fake. Point BRONTO_COGNITO_URL and BRONTO_BASE_URL at it
// (and BRONTO_INGEST_URL at <url>/ingest).
type Server struct {
	mu       sync.Mutex
	users    map[string]*user
	datasets []string
	keyNames []string
}

func New() *Server { return &Server{users: map[string]*user{}} }

// KeyNames returns the name of every key created through POST /api-keys.
func (s *Server) KeyNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.keyNames)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := r.Header.Get("X-Amz-Target"); t != "" {
		s.cognito(w, r, strings.TrimPrefix(t, "AWSCognitoIdentityProviderService."))
		return
	}
	switch {
	case r.URL.Path == "/__signup/inbox":
		s.inbox(w, r.URL.Query().Get("email"))
	case r.URL.Path == "/api-keys" && r.Method == http.MethodPost:
		var body struct {
			Name  string   `json:"name"`
			Roles []string `json:"roles"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Authorization") != "Bearer "+idToken {
			reply(w, http.StatusUnauthorized, map[string]any{"details": "Unauthorized"})
			return
		}
		s.keyNames = append(s.keyNames, body.Name)
		reply(w, http.StatusCreated, map[string]any{
			"id": "00000000-0000-4000-8000-000000000001", "name": body.Name, "api_key": APIKey, "roles": body.Roles})
	case r.Header.Get("X-BRONTO-API-KEY") != APIKey:
		reply(w, http.StatusUnauthorized, map[string]any{"details": "Invalid API key"})
	case r.URL.Path == "/logs" && r.Method == http.MethodGet:
		logs := []any{}
		for i, d := range s.datasets {
			logs = append(logs, map[string]any{
				"log_id": fmt.Sprintf("00000000-0000-4000-8000-0000000001%02d", i),
				"log":    d, "dataset": d, "collection": "default"})
		}
		reply(w, http.StatusOK, map[string]any{"logs": logs})
	case strings.HasPrefix(r.URL.Path, "/ingest") && r.Method == http.MethodPost:
		if d := r.Header.Get("x-bronto-dataset"); d != "" && !slices.Contains(s.datasets, d) {
			s.datasets = append(s.datasets, d)
		}
		reply(w, http.StatusOK, map[string]any{"accepted": 1})
	default:
		reply(w, http.StatusNotFound, map[string]any{"details": "signupmock: no route for " + r.Method + " " + r.URL.Path})
	}
}

func (s *Server) cognito(w http.ResponseWriter, r *http.Request, action string) {
	var body struct {
		Username, ConfirmationCode string
		UserAttributes             []struct{ Name, Value string }
		AuthParameters             map[string]string
		ChallengeResponses         map[string]string
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	fail := func(typ, msg string) {
		reply(w, http.StatusBadRequest, map[string]any{"__type": typ, "message": msg})
	}
	switch action {
	case "SignUp":
		if s.users[body.Username] != nil {
			fail("UsernameExistsException", "An account with the given email already exists.")
			return
		}
		u := &user{}
		for _, a := range body.UserAttributes {
			if a.Name == "name" {
				u.first = a.Value
			}
		}
		s.users[body.Username] = u
		reply(w, http.StatusOK, map[string]any{"UserConfirmed": false, "UserSub": "00000000-0000-4000-8000-000000000002",
			"CodeDeliveryDetails": map[string]any{"AttributeName": "email", "DeliveryMedium": "EMAIL", "Destination": "a***@e***"}})
	case "ConfirmSignUp":
		u := s.users[body.Username]
		switch {
		case u == nil:
			fail("UserNotFoundException", "Username/client id combination not found.")
		case body.ConfirmationCode != Code:
			fail("CodeMismatchException", "Invalid verification code provided, please try again.")
		default:
			u.confirmed = true
			reply(w, http.StatusOK, map[string]any{})
		}
	case "InitiateAuth":
		// The user id is the email here (the real pool answers a UUID), so
		// RespondToAuthChallenge can find the user without session state.
		reply(w, http.StatusOK, map[string]any{"ChallengeName": "PASSWORD_VERIFIER", "ChallengeParameters": map[string]string{ // #nosec G101 -- fake challenge
			"SRP_B": "5a17b0c2", "SALT": "0f1e2d3c4b5a6978", "SECRET_BLOCK": "ZmFrZSBzZWNyZXQgYmxvY2s=",
			"USER_ID_FOR_SRP": body.AuthParameters["USERNAME"]}})
	case "RespondToAuthChallenge":
		u := s.users[body.ChallengeResponses["USERNAME"]]
		switch {
		case u == nil || body.ChallengeResponses["PASSWORD_CLAIM_SIGNATURE"] == "":
			fail("NotAuthorizedException", "Incorrect username or password.")
		case !u.confirmed:
			fail("UserNotConfirmedException", "User is not confirmed.")
		default:
			reply(w, http.StatusOK, map[string]any{"AuthenticationResult": map[string]any{"IdToken": idToken}})
		}
	default:
		fail("UnknownOperationException", "signupmock: no "+action)
	}
}

// inbox answers GET /__signup/inbox?email=: the verification email, once
// the address has signed up.
func (s *Server) inbox(w http.ResponseWriter, email string) {
	u := s.users[email]
	if u == nil {
		reply(w, http.StatusOK, []any{})
		return
	}
	link := "https://app.eu.bronto.io/verify?confirmation_code=" + Code + "&email=" + email
	reply(w, http.StatusOK, []any{map[string]any{
		"from":    "Bronto <no-reply@bronto.io>",
		"subject": "Verify your email for Bronto",
		"link":    link,
		"body":    "Hi " + u.first + ",\n\nWelcome to Bronto! Click the link below to verify your email address:\n\n" + link + "\n",
	}})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
