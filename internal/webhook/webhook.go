// Package webhook serves POST /hooks/<name>: events from other systems (Home Assistant, a cron
// job, anything that can make an HTTP request) for the bot to check and pass on.
package webhook

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
)

// MaxBody is the largest request body accepted.
const MaxBody = 16 << 10

// ErrUnknownHook is what deliver returns for a hook that isn't configured.
var ErrUnknownHook = errors.New("unknown hook")

// Handler serves POST /hooks/{name}. Requests need "Authorization: Bearer <token>". The body is
// the event: JSON {"text": "...", "key": "..."}, or plain text with ?key=... The optional key says
// what the event is about (e.g. a camera), for the hook's cooldown. deliver takes the event:
// ErrUnknownHook for a hook that isn't configured, any other error while the bot can't take events
// (starting up).
func Handler(token string, deliver func(name, text, key string) error) http.Handler {
	want := []byte("Bearer " + token)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/{name}", func(w http.ResponseWriter, r *http.Request) {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			slog.Warn("hook request refused: wrong or missing token", "remote", r.RemoteAddr)
			reply(w, http.StatusUnauthorized, "unauthorized: send Authorization: Bearer <token>")
			return
		}
		text, key, err := readEvent(w, r)
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			reply(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		case err != nil:
			reply(w, http.StatusBadRequest, err.Error())
			return
		case strings.TrimSpace(text) == "":
			reply(w, http.StatusBadRequest, `empty event: send {"text": "..."} or plain text`)
			return
		}
		switch err := deliver(r.PathValue("name"), text, key); {
		case errors.Is(err, ErrUnknownHook):
			reply(w, http.StatusNotFound, "no hook named "+r.PathValue("name"))
			return
		case err != nil:
			reply(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		reply(w, http.StatusAccepted, "queued")
	})
	return mux
}

// readEvent gets the event text from a JSON body's "text" (or "message"), or a plain body, and its
// key from "key" in the JSON or ?key= in the URL.
func readEvent(w http.ResponseWriter, r *http.Request) (text, key string, err error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		return "", "", err
	}
	key = r.URL.Query().Get("key")
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/json" {
		return string(body), key, nil
	}
	var v struct {
		Text    string `json:"text"`
		Message string `json:"message"`
		Key     string `json:"key"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", "", errors.New(`invalid JSON: send {"text": "..."}`)
	}
	if v.Text == "" {
		v.Text = v.Message
	}
	if v.Key != "" {
		key = v.Key
	}
	return v.Text, key, nil
}

func reply(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}
