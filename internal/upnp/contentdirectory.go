package upnp

import (
	"net/http"
	"strconv"

	"nanodlna/internal/didl"
	"nanodlna/internal/library"
)

// handleContentDirectory serves the ContentDirectory:1 SOAP control endpoint.
func (s *Server) handleContentDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req, err := parseSOAPRequest(r, serviceContentDirectory)
	if err != nil {
		s.log.Warn("rejecting ContentDirectory request", "err", err, "remote", r.RemoteAddr)
		writeSOAPFault(w, errInvalidArgs, "Invalid Args")
		return
	}
	s.log.Debug("ContentDirectory action", "action", req.Action, "args", req.Args)

	switch req.Action {
	case "Browse":
		s.browse(w, req)
	case "GetSearchCapabilities":
		// Search is not implemented, so no search capabilities are advertised.
		writeSOAPResponse(w, serviceContentDirectory, req.Action, []soapArg{
			{Name: "SearchCaps", Value: ""},
		})
	case "GetSortCapabilities":
		writeSOAPResponse(w, serviceContentDirectory, req.Action, []soapArg{
			{Name: "SortCaps", Value: "dc:title"},
		})
	case "GetSystemUpdateID":
		writeSOAPResponse(w, serviceContentDirectory, req.Action, []soapArg{
			{Name: "Id", Value: strconv.FormatUint(uint64(s.lib.UpdateID()), 10)},
		})
	default:
		writeSOAPFault(w, errInvalidAction, "Invalid Action")
	}
}

// browse implements the Browse action.
func (s *Server) browse(w http.ResponseWriter, req *soapRequest) {
	base := s.BaseURL()

	objectID := req.Args["ObjectID"]
	if objectID == "" {
		objectID = "0"
	}
	browseFlag := req.Args["BrowseFlag"]
	if browseFlag == "" {
		browseFlag = "BrowseDirectChildren"
	}
	start := atoiDefault(req.Args["StartingIndex"], 0)
	count := atoiDefault(req.Args["RequestedCount"], 0)

	var (
		objects []didl.Object
		total   int
	)

	switch browseFlag {
	case "BrowseMetadata":
		node := s.nodeForObjectID(objectID)
		if node == nil {
			writeSOAPFault(w, errNoSuchObject, "No such object")
			return
		}
		objects = []didl.Object{s.didlObject(base, node)}
		total = 1

	case "BrowseDirectChildren":
		var children []*library.Node
		if objectID == "0" {
			if root := s.lib.Root(); root != nil {
				children = root.Children
			}
		} else {
			node := s.nodeForObjectID(objectID)
			if node == nil {
				writeSOAPFault(w, errNoSuchObject, "No such object")
				return
			}
			// Videos have no children. Returning an empty result is friendlier
			// than an error because some clients browse items speculatively.
			if node.Kind == library.KindContainer {
				children = node.Children
			}
		}
		total = len(children)
		for _, c := range sliceWindow(children, start, count) {
			objects = append(objects, s.didlObject(base, c))
		}

	default:
		writeSOAPFault(w, errInvalidArgs, "Invalid Args")
		return
	}

	updateID := strconv.FormatUint(uint64(s.lib.UpdateID()), 10)
	writeSOAPResponse(w, serviceContentDirectory, "Browse", []soapArg{
		{Name: "Result", Value: didl.Document(objects)},
		{Name: "NumberReturned", Value: strconv.Itoa(len(objects))},
		{Name: "TotalMatches", Value: strconv.Itoa(total)},
		{Name: "UpdateID", Value: updateID},
	})
}
