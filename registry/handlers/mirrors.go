package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	dcontext "github.com/docker/distribution/context"
	"github.com/docker/distribution/registry/proxy"
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
		"GET":    http.HandlerFunc(mirrorHandler.GetImageMirror),
		"DELETE": http.HandlerFunc(mirrorHandler.DeleteImageMirror),
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

	im := mh.registry.(proxy.ProxyRegistry).MirrorImage(mh.Repository.Named(), mh.Tag)
	p, _ := json.Marshal(im)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(p)))
	w.Write(p)
}

// GetImageMirror fetches the image manifest from the storage backend, if it exists.
func (mh *mirrorHandler) DeleteImageMirror(w http.ResponseWriter, r *http.Request) {
	dcontext.GetLogger(mh).Debug("DeleteImageMirror")

	mh.registry.(proxy.ProxyRegistry).DeleteMirrorImage(mh.Repository.Named(), mh.Tag)
	p := []byte("{OK}")

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(p)))
	w.Write(p)
}
