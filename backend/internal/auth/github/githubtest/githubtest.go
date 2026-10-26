// Package githubtest is an in-process fake of the parts of GitHub the store
// talks to: with a person's token the OAuth App's authorize and token
// endpoints, /user and /users/{login}; with the server's token
// (FAPPS_ADMIN_TOKEN) /repos/{o}/{r} and
// /repos/{o}/{r}/collaborators/{login}/permission, answered in the shapes
// measured against github.com on 2026-10-04 (auth/github.Permission). For
// tests only; nothing here is linked into the server.
package githubtest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

// User is a GitHub account.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Type  string `json:"type"`
}

// Server is the fake.
type Server struct {
	*httptest.Server
	ClientID     string
	ClientSecret string

	mu     sync.Mutex
	next   string            // the token the next authorize signs in as
	codes  map[string]string // code -> token
	users  map[string]User   // token -> user
	owners map[string]User   // lower(login) -> user or organization
	Hits   map[string]int    // path -> calls

	repos    map[string]int64             // lower("owner/name") -> repository id
	roles    map[string]map[string]string // lower("owner/name") -> lower(login) -> role
	server   map[string]map[string]bool   // server token -> lower("owner/name") it sees
	override *answer                      // the permission endpoint's next answers, when set
}

type answer struct {
	status int
	body   string
}

// New starts a fake GitHub.
func New() *Server {
	s := &Server{ClientID: "gh-client", ClientSecret: "gh-secret", codes: map[string]string{}, users: map[string]User{},
		owners: map[string]User{}, Hits: map[string]int{}, repos: map[string]int64{}, roles: map[string]map[string]string{},
		server: map[string]map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.token)
	mux.HandleFunc("GET /user", s.user)
	mux.HandleFunc("GET /users/{login}", s.owner)
	mux.HandleFunc("GET /repos/{owner}/{repo}", s.repo)
	mux.HandleFunc("GET /repos/{owner}/{repo}/collaborators/{login}/permission", s.permission)
	s.Server = httptest.NewServer(mux)
	return s
}

func random() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddUser registers an account and returns its token.
func (s *Server) AddUser(id int64, login, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := "gho_" + random()
	u := User{ID: id, Login: login, Name: name, Type: "User"}
	s.users[token] = u
	s.owners[strings.ToLower(login)] = u
	return token
}

// AddOrg registers an organization.
func (s *Server) AddOrg(id int64, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owners[strings.ToLower(login)] = User{ID: id, Login: login, Type: "Organization"}
}

// AddRepo registers a private repository "owner/name" that only the server
// tokens given it by ServerToken see.
func (s *Server) AddRepo(fullName string, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[strings.ToLower(fullName)] = id
}

// SetRole gives login a role on the repository: admin, maintain, write,
// triage, read, or "" (not a collaborator).
func (s *Server) SetRole(fullName, login, role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := strings.ToLower(fullName)
	if s.roles[k] == nil {
		s.roles[k] = map[string]string{}
	}
	s.roles[k][strings.ToLower(login)] = role
}

// ServerToken makes a token that stands for the server's fine-grained token
// (Metadata: read): it sees the named repositories and nothing else. With
// no repository it sees none, like a token the organization has not
// approved yet.
func (s *Server) ServerToken(fullNames ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := "github_pat_" + random()
	s.server[token] = map[string]bool{}
	for _, n := range fullNames {
		s.server[token][strings.ToLower(n)] = true
	}
	return token
}

// AnswerPermission makes the permission endpoint answer status and body
// (raw JSON) from now on, whoever asks; status 0 restores the real answers.
func (s *Server) AnswerPermission(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == 0 {
		s.override = nil
		return
	}
	s.override = &answer{status: status, body: body}
}

// SignInAs makes the next authorize sign in with this token's account.
func (s *Server) SignInAs(token string) {
	s.mu.Lock()
	s.next = token
	s.mu.Unlock()
}

// Code issues a sign-in code for a token directly (tests that skip the
// browser redirect).
func (s *Server) Code(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := random()
	s.codes[c] = token
	return c
}

func (s *Server) hit(r *http.Request) {
	s.mu.Lock()
	s.Hits[r.URL.Path]++
	s.mu.Unlock()
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	s.hit(r)
	q := r.URL.Query()
	if q.Get("client_id") != s.ClientID || q.Get("scope") != "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	token := s.next
	s.mu.Unlock()
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || token == "" {
		http.Error(w, "no redirect or nobody to sign in", http.StatusBadRequest)
		return
	}
	v := u.Query()
	v.Set("code", s.Code(token))
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.hit(r)
	_ = r.ParseForm()
	id, secret := r.Form.Get("client_id"), r.Form.Get("client_secret")
	if u, p, ok := r.BasicAuth(); ok {
		id, secret = u, p
	}
	if id != s.ClientID || secret != s.ClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect_client_credentials"})
		return
	}
	s.mu.Lock()
	token, ok := s.codes[r.Form.Get("code")]
	delete(s.codes, r.Form.Get("code"))
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token, "token_type": "bearer", "scope": ""})
}

func (s *Server) caller(r *http.Request) (User, string, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[token]
	return u, token, ok
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	s.hit(r)
	u, _, ok := s.caller(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) owner(w http.ResponseWriter, r *http.Request) {
	s.hit(r)
	s.mu.Lock()
	o, ok := s.owners[strings.ToLower(r.PathValue("login"))]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// sees reports whether the request's server token sees the repository;
// known=false for a token that is not a server token.
func (s *Server) sees(r *http.Request, fullName string) (sees, known bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	repos, ok := s.server[token]
	if !ok {
		return false, false
	}
	_, exists := s.repos[strings.ToLower(fullName)]
	return exists && repos[strings.ToLower(fullName)], true
}

func (s *Server) repo(w http.ResponseWriter, r *http.Request) {
	s.hit(r)
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	sees, known := s.sees(r, full)
	switch {
	case !known:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
	case !sees:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	default:
		s.mu.Lock()
		id := s.repos[strings.ToLower(full)]
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "full_name": full, "private": true})
	}
}

// legacy is GitHub's base role for a role: maintain answers write, triage
// answers read.
var legacy = map[string]string{"admin": "admin", "maintain": "write", "write": "write", "triage": "read", "read": "read", "": "none"}

func (s *Server) permission(w http.ResponseWriter, r *http.Request) {
	s.hit(r)
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	login := r.PathValue("login")
	sees, known := s.sees(r, full)
	if !known {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	if !sees {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	s.mu.Lock()
	o := s.override
	u, isUser := s.owners[strings.ToLower(login)]
	role := s.roles[strings.ToLower(full)][strings.ToLower(login)]
	s.mu.Unlock()
	if o != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(o.status)
		_, _ = w.Write([]byte(o.body))
		return
	}
	if !isUser || u.Type != "User" {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": login + " is not a user"})
		return
	}
	flags := map[string]bool{
		"admin":    role == "admin",
		"maintain": role == "admin" || role == "maintain",
		"push":     role == "admin" || role == "maintain" || role == "write",
		"triage":   role == "admin" || role == "maintain" || role == "write" || role == "triage",
		"pull":     role != "",
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"permission": legacy[role], "role_name": role,
		"user": map[string]any{"login": u.Login, "id": u.ID, "type": "User", "permissions": flags, "role_name": role},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
