package upnp

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nanodlna/internal/didl"
)

// GENA eventing constants.
const (
	nsEvent             = "urn:schemas-upnp-org:event-1-0"
	defaultSubscription = 1800 * time.Second
	maxSubscription     = 1800 * time.Second
	eventNotifyTimeout  = 5 * time.Second
)

// subscription is a GENA event subscription held for one client.
type subscription struct {
	sid      string
	service  string
	callback string
	expires  time.Time
	seq      uint32
}

// handleEvent implements the GENA event subscription URL of a service.
//
// Eventing is optional for a media server and VLC does not use it, but some
// television firmwares subscribe before they will browse a server, so the
// endpoint answers properly and pushes SystemUpdateID changes after a rescan.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	service := serviceContentDirectory
	if strings.HasPrefix(r.URL.Path, "/ConnectionManager/") {
		service = serviceConnectionManager
	}

	switch r.Method {
	case "SUBSCRIBE":
		s.handleSubscribe(w, r, service)
	case "UNSUBSCRIBE":
		s.handleUnsubscribe(w, r)
	default:
		w.Header().Set("Allow", "SUBSCRIBE, UNSUBSCRIBE")
		http.Error(w, "this URL accepts SUBSCRIBE and UNSUBSCRIBE only", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request, service string) {
	timeout := parseSubscriptionTimeout(r.Header.Get("TIMEOUT"))

	if sid := strings.TrimSpace(r.Header.Get("SID")); sid != "" {
		s.subsMu.Lock()
		sub, ok := s.subs[sid]
		if ok {
			sub.expires = time.Now().Add(timeout)
		}
		s.subsMu.Unlock()

		if !ok {
			http.Error(w, "unknown subscription", http.StatusPreconditionFailed)
			return
		}
		writeSubscribeResponse(w, sid, timeout)
		return
	}

	callback := firstCallback(r.Header.Get("CALLBACK"))
	if callback == "" {
		http.Error(w, "CALLBACK header is required", http.StatusPreconditionFailed)
		return
	}
	if nt := r.Header.Get("NT"); nt != "" && nt != "upnp:event" {
		http.Error(w, "unsupported NT header", http.StatusPreconditionFailed)
		return
	}

	sid, err := newSubscriptionID()
	if err != nil {
		http.Error(w, "cannot allocate a subscription", http.StatusInternalServerError)
		return
	}

	sub := &subscription{
		sid:      sid,
		service:  service,
		callback: callback,
		expires:  time.Now().Add(timeout),
	}
	s.subsMu.Lock()
	s.subs[sid] = sub
	// Take a private copy while the lock is held: notifySubscribers mutates the
	// stored subscription's sequence number, so the initial event must not
	// share it.
	initial := *sub
	s.subsMu.Unlock()

	// The response must reach the client before the initial event, so the event
	// is pushed from a separate goroutine.
	writeSubscribeResponse(w, sid, timeout)
	go s.sendEvent(&initial, s.initialEventBody(service))
}

func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSpace(r.Header.Get("SID"))
	if sid == "" {
		http.Error(w, "SID header is required", http.StatusPreconditionFailed)
		return
	}
	s.subsMu.Lock()
	delete(s.subs, sid)
	s.subsMu.Unlock()

	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

func writeSubscribeResponse(w http.ResponseWriter, sid string, timeout time.Duration) {
	w.Header().Set("SID", sid)
	w.Header().Set("TIMEOUT", "Second-"+strconv.Itoa(int(timeout.Seconds())))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// notifySubscribers pushes an event to every live subscription. It is called
// after a rescan so that clients refresh their view of the content.
func (s *Server) notifySubscribers() {
	s.subsMu.Lock()
	now := time.Now()
	pending := make([]*subscription, 0, len(s.subs))
	for sid, sub := range s.subs {
		if now.After(sub.expires) {
			delete(s.subs, sid)
			continue
		}
		sub.seq++
		pending = append(pending, &subscription{
			sid:      sub.sid,
			service:  sub.service,
			callback: sub.callback,
			seq:      sub.seq,
		})
	}
	s.subsMu.Unlock()

	for _, sub := range pending {
		if sub.service != serviceContentDirectory {
			continue
		}
		go s.sendEvent(sub, s.initialEventBody(sub.service))
	}
}

func (s *Server) initialEventBody(service string) string {
	if service == serviceConnectionManager {
		return eventBody([][2]string{
			{"SourceProtocolInfo", sourceProtocolInfo()},
			{"SinkProtocolInfo", ""},
			{"CurrentConnectionIDs", "0"},
		})
	}
	return eventBody([][2]string{
		{"SystemUpdateID", strconv.FormatUint(uint64(s.lib.UpdateID()), 10)},
	})
}

func eventBody(properties [][2]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<e:propertyset xmlns:e="` + nsEvent + `">`)
	for _, p := range properties {
		b.WriteString("<e:property><" + p[0] + ">" + didl.EscapeText(p[1]) + "</" + p[0] + "></e:property>")
	}
	b.WriteString(`</e:propertyset>`)
	return b.String()
}

func (s *Server) sendEvent(sub *subscription, body string) {
	req, err := http.NewRequest("NOTIFY", sub.callback, strings.NewReader(body))
	if err != nil {
		s.log.Debug("cannot build event request", "sid", sub.sid, "err", err)
		return
	}
	if u, err := url.Parse(sub.callback); err == nil {
		req.Host = u.Host
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("NT", "upnp:event")
	req.Header.Set("NTS", "upnp:propchange")
	req.Header.Set("SID", sub.sid)
	req.Header.Set("SEQ", strconv.FormatUint(uint64(sub.seq), 10))

	client := &http.Client{Timeout: eventNotifyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		s.log.Debug("event delivery failed", "sid", sub.sid, "callback", sub.callback, "err", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// parseSubscriptionTimeout reads a GENA TIMEOUT header such as "Second-1800".
func parseSubscriptionTimeout(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return defaultSubscription
	}
	if strings.Contains(strings.ToLower(header), "infinite") {
		return maxSubscription
	}
	if i := strings.IndexByte(header, '-'); i >= 0 {
		if secs, err := strconv.Atoi(strings.TrimSpace(header[i+1:])); err == nil && secs > 0 {
			d := time.Duration(secs) * time.Second
			if d > maxSubscription {
				return maxSubscription
			}
			return d
		}
	}
	return defaultSubscription
}

// firstCallback extracts the first URL from a CALLBACK header, which may hold
// several angle-bracketed URLs.
func firstCallback(header string) string {
	header = strings.TrimSpace(header)
	for {
		start := strings.IndexByte(header, '<')
		if start < 0 {
			return ""
		}
		end := strings.IndexByte(header[start:], '>')
		if end < 0 {
			return ""
		}
		candidate := strings.TrimSpace(header[start+1 : start+end])
		if u, err := url.Parse(candidate); err == nil && u.Scheme != "" && u.Host != "" {
			return candidate
		}
		header = header[start+end+1:]
	}
}

func newSubscriptionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("uuid:%s", formatUUID(b[:])), nil
}
