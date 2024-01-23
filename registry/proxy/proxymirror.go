package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/distribution"
	"github.com/docker/distribution/configuration"
	dcontext "github.com/docker/distribution/context"
	"github.com/docker/distribution/manifest/manifestlist"
	v2 "github.com/docker/distribution/registry/api/v2"
	client "github.com/docker/distribution/registry/client"
	"github.com/docker/distribution/registry/client/auth"
	"github.com/docker/distribution/registry/client/auth/challenge"
	"github.com/docker/distribution/registry/client/transport"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

var once sync.Once

type ProxyRegistry interface {
	MirrorImage(name reference.Named, tag string) *v2.ImageMirror
	DeleteMirrorImage(name reference.Named, tag string)
}

type MirrorController struct {
	ctx            context.Context
	mirrors        map[string]*v2.ImageMirror
	url            url.URL
	authChallenger authChallenger

	embedded distribution.Namespace

	configuration.Mirror

	mux sync.RWMutex
}

func NewMirrorController(ctx context.Context, config *configuration.Configuration, embedded distribution.Namespace) (ProxyRegistry, error) {
	parts := []string{}
	if config.HTTP.Addr == "" {
		parts = []string{"127.0.0.1", "5000"}
	} else {
		parts = strings.Split(config.HTTP.Addr, ":")
		if parts[0] == "" {
			parts[0] = "127.0.0.1"
		}
	}
	url, _ := url.Parse(fmt.Sprintf("http://%s:%s", parts[0], parts[1]))
	c := &MirrorController{
		ctx:      ctx,
		mirrors:  make(map[string]*v2.ImageMirror),
		url:      *url,
		embedded: embedded,
	}
	go c.gc()
	return c, nil
}

func (c *MirrorController) gc() {
	ticker := time.NewTicker(5 * time.Minute)
	for {
		select {
		case <-ticker.C:
			c.mux.Lock()
			t := time.Now().Add(-7 * 24 * time.Hour)
			needDeleted := []string{}
			for ref, im := range c.mirrors {
				if im.Phase == v2.Mirrored && t.After(im.CreateTime) {
					needDeleted = append(needDeleted, ref)
				}
			}
			for _, ref := range needDeleted {
				delete(c.mirrors, ref)
			}
			c.mux.Unlock()
		case <-c.ctx.Done():
			break
		}
	}
}

func (c *MirrorController) configAuth() error {
	var authErr error
	once.Do(func() {
		cs, err := configureAuth(c.Username, c.Password, c.url.String())
		if err != nil {
			authErr = err
			return
		}
		c.authChallenger = &remoteAuthChallenger{
			remoteURL: c.url,
			cm:        challenge.NewSimpleManager(),
			cs:        cs,
		}
	})
	c.authChallenger.tryEstablishChallenges(context.TODO())
	return authErr
}

func (c *MirrorController) MirrorImage(name reference.Named, tag string) *v2.ImageMirror {
	im := c.getim(name, tag)
	if im.IsPending() {
		go c.mirrorimages(im)
	}
	return im
}

func (c *MirrorController) DeleteMirrorImage(name reference.Named, tag string) {
	ref := name.Name() + ":" + tag
	c.delim(ref)
}

func (c *MirrorController) getim(name reference.Named, tag string) *v2.ImageMirror {
	ref := name.Name() + ":" + tag
	c.mux.RLock()
	im, ok := c.mirrors[ref]
	c.mux.RUnlock()
	if !ok {
		im = v2.NewImageMirror(name, tag)
		c.mux.Lock()
		if _, ok := c.mirrors[ref]; !ok {
			c.mirrors[ref] = im
		} else {
			im = c.mirrors[ref]
		}
		c.mux.Unlock()
	}
	return im
}

func (c *MirrorController) delim(ref string) {
	c.mux.Lock()
	defer c.mux.Unlock()
	delete(c.mirrors, ref)
}

func (c *MirrorController) mirrorimages(im *v2.ImageMirror) {
	c.configAuth()
	a := c.authChallenger

	tkopts := auth.TokenHandlerOptions{
		Transport:   http.DefaultTransport,
		Credentials: a.credentialStore(),
		Scopes: []auth.Scope{
			auth.RepositoryScope{
				Repository: im.Name,
				Actions:    []string{"pull"},
			},
		},
		Logger: dcontext.GetLogger(c.ctx),
	}

	tr := transport.NewTransport(http.DefaultTransport,
		auth.NewAuthorizer(a.challengeManager(),
			auth.NewTokenHandlerWithOptions(tkopts)))

	repo, err := client.NewRepository(im.Named(), c.url.String(), tr, im)
	if err != nil {
		im.UpdateStatue(v2.NameInvalid, fmt.Sprintf("get registry repository error: %s", err))
		return
	}
	manifestService, err := repo.Manifests(context.TODO())
	if err != nil {
		im.UpdateStatue(v2.Unknown, fmt.Sprintf("create manifests service error: %s", err))
		return
	}

	localrepo, err := c.embedded.Repository(context.TODO(), im.Named())
	if err != nil {
		im.UpdateStatue(v2.Unknown, fmt.Sprintf("get local repository error: %s", err))
	}
	localManifestService, err := localrepo.Manifests(context.TODO())
	if err != nil {
		im.UpdateStatue(v2.Unknown, fmt.Sprintf("create local manifests service error: %s", err))
		return
	}

	desc, err := repo.Tags(context.TODO()).Get(context.TODO(), im.Tag)
	if err != nil {
		im.UpdateStatue(v2.TagNotFound, "")
		return
	}
	dcontext.GetLogger(c.ctx).Debugf("get image tag digest: %s", desc.Digest)
	manifest, err := localManifestService.Get(context.TODO(), desc.Digest)
	if err != nil {
		im.UpdateStatue(v2.Unknown, fmt.Sprintf("get manifests error: %s", err))
		return
	}

	switch desc.MediaType {
	case "application/vnd.docker.distribution.manifest.v2+json":
		err = im.MirrorImage(repo, localrepo, desc, manifest)
		if err != nil {
			im.UpdateStatue(v2.Unknown, fmt.Sprintf("mirror image blobs error: %s", err))
		}
	case "application/vnd.oci.image.index.v1+json":
		descs := manifest.(*manifestlist.DeserializedManifestList)
		for _, md := range descs.Manifests {
			d := md.Descriptor
			d.Platform = &v1.Platform{
				Architecture: md.Platform.Architecture,
				OS:           md.Platform.OS,
			}
			switch d.MediaType {
			case "application/vnd.oci.image.manifest.v1+json":
				m, err := manifestService.Get(context.TODO(), d.Digest)
				if err != nil {
					im.UpdateStatue(v2.Unknown, fmt.Sprintf("get manifests error: %s", err))
					return
				}
				err = im.MirrorImage(repo, localrepo, d, m)
				if err != nil {
					im.UpdateStatue(v2.Unknown, fmt.Sprintf("mirror image blobs error: %s", err))
					return
				}
			default:
				im.UpdateStatue(v2.UnknownMediaType, fmt.Sprintf("unknown mediatype: %s", d.MediaType))
				return
			}
		}
	default:
		im.UpdateStatue(v2.UnknownMediaType, fmt.Sprintf("unknown mediatype: %s", desc.MediaType))
		return
	}

	im.UpdateStatue(v2.Success, "")
}
