package upnp

import (
	"net/http"
	"sort"
	"strings"

	"nanodlna/internal/library"
)

// handleConnectionManager serves the ConnectionManager:1 SOAP control
// endpoint. Media servers only need to answer the informational actions.
func (s *Server) handleConnectionManager(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req, err := parseSOAPRequest(r, serviceConnectionManager)
	if err != nil {
		s.log.Warn("rejecting ConnectionManager request", "err", err, "remote", r.RemoteAddr)
		writeSOAPFault(w, errInvalidArgs, "Invalid Args")
		return
	}
	s.log.Debug("ConnectionManager action", "action", req.Action, "args", req.Args)

	switch req.Action {
	case "GetProtocolInfo":
		writeSOAPResponse(w, serviceConnectionManager, req.Action, []soapArg{
			{Name: "Source", Value: sourceProtocolInfo()},
			{Name: "Sink", Value: ""},
		})
	case "GetCurrentConnectionIDs":
		// A media server without an active transport uses connection id 0.
		writeSOAPResponse(w, serviceConnectionManager, req.Action, []soapArg{
			{Name: "ConnectionIDs", Value: "0"},
		})
	case "GetCurrentConnectionInfo":
		writeSOAPResponse(w, serviceConnectionManager, req.Action, []soapArg{
			{Name: "RcsID", Value: "0"},
			{Name: "AVTransportID", Value: "0"},
			{Name: "ProtocolInfo", Value: ""},
			{Name: "PeerConnectionManager", Value: ""},
			{Name: "PeerConnectionID", Value: "-1"},
			{Name: "Direction", Value: "Output"},
			{Name: "Status", Value: "OK"},
		})
	default:
		writeSOAPFault(w, errInvalidAction, "Invalid Action")
	}
}

// sourceProtocolInfo lists every protocolInfo this server can deliver, which is
// what clients use to decide whether a file is playable directly.
func sourceProtocolInfo() string {
	seen := map[string]bool{}
	var out []string
	add := func(mime string) {
		if mime == "" || seen[mime] {
			return
		}
		seen[mime] = true
		out = append(out, "http-get:*:"+mime+":*")
	}
	for _, mime := range library.VideoExts {
		add(mime)
	}
	for _, mime := range library.SubExts {
		add(mime)
	}
	add("image/png")
	sort.Strings(out)
	return strings.Join(out, ",")
}
