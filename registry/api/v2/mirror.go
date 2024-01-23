package v2

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/distribution"
	"github.com/opencontainers/go-digest"
)

type Phase string

const (
	Pending   Phase = "Pending"
	Mirroring Phase = "Mirroring"
	Mirrored  Phase = "Mirrored"
)

type Code int

const (
	Success          Code = 100
	NameInvalid      Code = 101
	NameNotFound     Code = 102
	TagInvalid       Code = 103
	TagNotFound      Code = 104
	Unknown          Code = 105
	UnknownMediaType Code = 106
)

type Blob struct {
	Precent int    `json:"precent"`
	Size    int64  `json:"size"`
	Error   error  `json:"error"`
	Speed   string `json:"speed"`
}

type Image struct {
	Architecture string                  `json:"architecture"`
	OS           string                  `json:"os"`
	Digest       digest.Digest           `json:"digest"`
	Blobs        map[digest.Digest]*Blob `json:"blobs"`
}

func imagetype(arch, os string) string {
	return fmt.Sprintf("%s-%s", os, arch)
}

type ImageMirror struct {
	Name       string    `json:"name"`
	Tag        string    `json:"tag"`
	CreateTime time.Time `json:"createTime"`
	Phase      Phase     `json:"phase"`
	Message    string    `json:"message"`
	Code       Code      `json:"code"`
	Images     []*Image  `json:"images"`

	mux          sync.Mutex
	name         reference.Named
	blobToImages map[digest.Digest]int
}

func NewImageMirror(name reference.Named, tag string) *ImageMirror {
	return &ImageMirror{
		Name:         name.Name(),
		Tag:          tag,
		CreateTime:   time.Now(),
		Phase:        Pending,
		name:         name,
		blobToImages: make(map[digest.Digest]int),
	}
}

func (im *ImageMirror) Named() reference.Named {
	return im.name
}

func (im *ImageMirror) IsPending() bool {
	im.mux.Lock()
	im.mux.Unlock()
	isPending := im.Phase == Pending
	if isPending {
		im.Phase = Mirroring
	}
	return isPending
}

func (im *ImageMirror) UpdateStatue(code Code, message string) {
	im.Code = code
	if message != "" {
		im.Message = message
		return
	}
	switch code {
	case NameInvalid:
		im.Message = "repository name invalid"
	case TagNotFound:
		im.Message = "tag not found"
	case Success:
		im.Message = "mirror successed"
		im.Phase = Mirrored
	}
}

func (im *ImageMirror) UpdateImagePrecent(dig digest.Digest, size int64) error {
	idx, ok := im.blobToImages[dig]
	if !ok {
		return fmt.Errorf("ImageMirror %s: not found digest %s", im.name, dig)
	}
	if idx >= len(im.Images) {
		return fmt.Errorf("ImageMirror %s: digest %s found images index lager than len(im.Images)", im.name, dig)
	}
	layer, ok := im.Images[idx].Blobs[dig]
	if !ok {
		return fmt.Errorf("ImageMirror %s: not found image layer for digest %s", im.name, dig)
	}
	layer.Precent = int(size * 100 / layer.Size)
	return nil
}

func (im *ImageMirror) MirrorImage(repo, localrepo distribution.Repository, desc distribution.Descriptor, manifest distribution.Manifest) error {
	img := &Image{
		Digest: desc.Digest,
		Blobs:  make(map[digest.Digest]*Blob),
	}
	im.Images = append(im.Images, img)
	if desc.Platform != nil {
		img.Architecture = desc.Platform.Architecture
		img.OS = desc.Platform.OS
	}
	wg := sync.WaitGroup{}
	descs := manifest.References()
	getBlobError := false
	for _, d := range descs {
		switch d.MediaType {
		case "application/vnd.docker.container.image.v1+json",
			"application/vnd.oci.image.config.v1+json",
			"application/vnd.docker.image.rootfs.diff.tar.gzip",
			"application/vnd.in-toto+json",
			"application/vnd.oci.image.layer.v1.tar+gzip":
			img.Blobs[d.Digest] = &Blob{Size: d.Size, Precent: 100}
			_, err := localrepo.Blobs(context.TODO()).Stat(context.TODO(), d.Digest)
			if err != nil {
				im.blobToImages[d.Digest] = len(im.Images) - 1
				img.Blobs[d.Digest].Precent = 0
				wg.Add(1)
				go func(img *Image, dgst digest.Digest) {
					defer wg.Done()
					_, err := repo.Blobs(context.TODO()).Get(context.TODO(), dgst)
					if err != nil {
						img.Blobs[dgst].Error = err
						getBlobError = true
					}
				}(img, d.Digest)
			}
		default:
			return fmt.Errorf("unknown mediatype %s", d.MediaType)
		}
	}
	wg.Wait()
	if getBlobError {
		return fmt.Errorf("download blob error")
	}
	return nil
}
