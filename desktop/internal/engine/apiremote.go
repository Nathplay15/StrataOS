package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"time"

	"strataos-sync/internal/api"
)

// APIRemote is the NAS through the sync API.
type APIRemote struct{ C *api.Client }

func fromAPI(e api.Entry) RemoteEntry {
	return RemoteEntry{Path: e.Path, Dir: e.Dir, Size: e.Size, MTime: e.MTime, Version: e.Version, Hash: e.Hash, ID: e.ID}
}

// mapErr turns API errors into the engine's.
func mapErr(err error, rel string) error {
	if err == nil {
		return nil
	}
	if c, ok := api.ConflictOf(err); ok {
		var cur *RemoteEntry
		if c.Current != nil {
			e := fromAPI(*c.Current)
			e.Path = rel // the NAS names it relative to the request
			cur = &e
		}
		return &Conflict{Msg: c.Msg, Current: cur}
	}
	switch api.StatusOf(err) {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	if errors.Is(err, api.ErrChanged) {
		return ErrChanged
	}
	return err
}

func (a APIRemote) Changes(ctx context.Context, folder, cursor string, wait time.Duration) (Changes, error) {
	c, err := a.C.Changes(ctx, folder, cursor, wait)
	if err != nil {
		return Changes{}, mapErr(err, "")
	}
	out := Changes{Cursor: c.Cursor, Reset: c.Reset, More: c.More}
	for _, e := range c.Changes {
		if e.Deleted {
			out.Deleted = append(out.Deleted, e.Path)
		} else {
			out.Entries = append(out.Entries, fromAPI(e))
		}
	}
	return out, nil
}

func (a APIRemote) Hashes(ctx context.Context, folder string, paths []string) (map[string]string, error) {
	h, err := a.C.Hashes(ctx, folder, paths)
	return h, mapErr(err, "")
}

func (a APIRemote) Download(ctx context.Context, folder, rel, version string, offset int64, w io.Writer) (string, int64, error) {
	v, n, err := a.C.Download(ctx, folder, rel, version, offset, w)
	return v, n, mapErr(err, rel)
}

func (a APIRemote) Upload(ctx context.Context, folder string, u UploadReq) (RemoteEntry, error) {
	res, err := a.C.UploadFile(ctx, api.Upload{Folder: folder, Rel: u.Rel, Size: u.Size, MTime: u.MTime, IfMatch: u.IfMatch,
		Body: u.Body, ResumeID: u.ResumeID, OnID: u.OnID, Progress: u.Progress})
	if err != nil {
		err = mapErr(err, u.Rel)
		var c *Conflict
		if errors.As(err, &c) {
			c.UploadID = res.ID
		}
		return RemoteEntry{}, err
	}
	return fromAPI(res.Entry), nil
}

func (a APIRemote) Commit(ctx context.Context, folder, uploadID, rel, ifMatch string) (RemoteEntry, error) {
	e, err := a.C.Commit(ctx, uploadID, path.Base(rel), ifMatch, rel)
	if err != nil {
		return RemoteEntry{}, mapErr(err, rel)
	}
	return fromAPI(e), nil
}

func (a APIRemote) Mkdir(ctx context.Context, folder, rel string) (RemoteEntry, error) {
	e, err := a.C.Mkdir(ctx, folder, rel)
	if err != nil {
		return RemoteEntry{}, mapErr(err, rel)
	}
	e.Path = rel
	return fromAPI(e), nil
}

func (a APIRemote) Move(ctx context.Context, folder, from, to, ifMatch string) (RemoteEntry, error) {
	e, err := a.C.Move(ctx, folder, from, to, ifMatch)
	if err != nil {
		return RemoteEntry{}, mapErr(err, from)
	}
	e.Path = to
	return fromAPI(e), nil
}

func (a APIRemote) Delete(ctx context.Context, folder, rel, ifMatch string) error {
	_, err := a.C.Delete(ctx, folder, rel, ifMatch)
	return mapErr(err, rel)
}
