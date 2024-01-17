package handlers

import (
	"fmt"
	"net/http"

	dcontext "github.com/docker/distribution/context"
	v2 "github.com/docker/distribution/registry/api/v2"
	"github.com/gorilla/handlers"
)

// manifestDispatcher takes the request context and builds the
// appropriate handler for handling manifest requests.
func mirrorDispatcher(ctx *Context, r *http.Request) http.Handler {
	mirrorHandler := &mirrorHandler{
		Context: ctx,
	}
	reference := getReference(ctx)
	mirrorHandler.Tag = reference

	mhandler := handlers.MethodHandler{
		"GET": http.HandlerFunc(mirrorHandler.GetImageMirror),
	}

	return mhandler
}

// manifestHandler handles http operations on image manifests.
type mirrorHandler struct {
	*Context

	Tag string
}

// GetImageMirror fetches the image manifest from the storage backend, if it exists.
func (mh *mirrorHandler) GetImageMirror(w http.ResponseWriter, r *http.Request) {
	dcontext.GetLogger(mh).Debug("GetImageMirror")

	if mh.Tag == "notfound" {
		mh.Errors = append(mh.Errors, v2.ErrorCodeNameUnknown)
		return
	}

	p := []byte(fmt.Sprintf("{\"name\":\"%s\",\"tag\":\"%s\",\"status\":\"mirrored\"}", mh.Repository.Named(), mh.Tag))
	ct := "application/json"

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprint(len(p)))
	w.Write(p)
}
