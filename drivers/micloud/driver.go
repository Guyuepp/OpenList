// Package micloud is an OpenList driver for Xiaomi Cloud Drive (i.mi.com),
// built on github.com/Guyuepp/xiaomi-drive-go.
package micloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Guyuepp/xiaomi-drive-go"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type MiCloud struct {
	model.Storage
	Addition

	client *xiaomidrive.Client

	// saveMu serialises writes of the addition (tokens, placeholders).
	saveMu sync.Mutex
	// phMu guards ph: parent folder ID -> lower-case name -> empty file.
	phMu sync.Mutex
	ph   map[string]map[string]placeholder
}

// persist applies a change to the addition and saves the storage record.
func (d *MiCloud) persist(change func()) {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	change()
	op.MustSaveDriverStorage(d)
}

func (d *MiCloud) Config() driver.Config {
	return config
}

func (d *MiCloud) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *MiCloud) Init(ctx context.Context) error {
	if d.RootFolderID == "" {
		d.RootFolderID = xiaomidrive.RootID
	}
	d.UserID = strings.TrimSpace(d.UserID)
	d.PassToken = strings.TrimSpace(d.PassToken)
	if d.UserID == "" || d.PassToken == "" {
		return errors.New("user_id and pass_token are required; get them with `micloud login qr` and `micloud export`")
	}
	d.client = xiaomidrive.NewClient(xiaomidrive.WithStore(&additionStore{d: d}))
	d.loadPlaceholders()
	// Always start from a freshly issued serviceToken: it validates the
	// passToken and avoids relying on a token persisted long ago.
	if err := d.client.Session().Refresh(ctx); err != nil {
		if errors.Is(err, xiaomidrive.ErrUnauthorized) {
			return fmt.Errorf("pass_token rejected, log in again with `micloud login qr`: %w", err)
		}
		return err
	}
	// The keep-alive outlives Init's context; Drop stops it.
	d.client.StartKeepAlive(context.Background())
	return nil
}

func (d *MiCloud) Drop(ctx context.Context) error {
	if d.client != nil {
		d.client.Close()
	}
	return nil
}

func (d *MiCloud) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.client.List(ctx, toFile(dir))
	if err != nil {
		return nil, mapErr(err)
	}
	objs := make([]model.Obj, len(files))
	for i, f := range files {
		objs[i] = wrap(f)
	}
	return d.mergePlaceholders(dir.GetID(), objs), nil
}

// linkMargin is subtracted from the URL lifetime so OpenList never hands out
// a link that is about to expire.
const linkMargin = 10 * time.Minute

func (d *MiCloud) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if _, ok := asPlaceholder(file); ok {
		if args.Redirect {
			return nil, errPlaceholderRedirect
		}
		return placeholderLink(), nil
	}
	l, err := d.client.Link(ctx, file.GetID())
	if err != nil {
		return nil, mapErr(err)
	}
	exp := time.Hour
	if !l.Expires.IsZero() {
		if until := time.Until(l.Expires) - linkMargin; until > time.Minute {
			exp = until
		} else {
			exp = time.Minute
		}
	}
	return &model.Link{URL: l.URL, Header: l.Header, Expiration: &exp}, nil
}

func (d *MiCloud) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	f, err := d.client.MakeDir(ctx, parentDir.GetID(), dirName)
	if err != nil {
		return nil, mapErr(err)
	}
	return wrap(f), nil
}

// conflict reports whether dir already holds an item named name, on the
// server or as a local empty file. The service compares names
// case-insensitively.
func (d *MiCloud) conflict(ctx context.Context, dir model.Obj, name string) error {
	children, err := d.client.List(ctx, toFile(dir))
	if err != nil {
		return mapErr(err)
	}
	for _, c := range children {
		if strings.EqualFold(c.Name, name) {
			return fmt.Errorf("%w: %q", errs.ObjectAlreadyExists, c.Name)
		}
	}
	return d.placeholderConflict(dir.GetID(), name)
}

func (d *MiCloud) placeholderConflict(parentID, name string) error {
	d.phMu.Lock()
	defer d.phMu.Unlock()
	if d.phExistsLocked(parentID, name) {
		return fmt.Errorf("%w: %q (empty file)", errs.ObjectAlreadyExists, name)
	}
	return nil
}

func (d *MiCloud) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if p, ok := asPlaceholder(srcObj); ok {
		return d.placePlaceholder(ctx, p, dstDir.GetID(), p.Name, false)
	}
	// MOVE silently renames on a clash ("name(1).ext"), which would desync
	// OpenList's view; refuse instead, like a file system would.
	if err := d.conflict(ctx, dstDir, srcObj.GetName()); err != nil {
		return nil, err
	}
	src := toFile(srcObj)
	if err := d.client.Move(ctx, []*xiaomidrive.File{src}, dstDir.GetID()); err != nil {
		return nil, mapErr(err)
	}
	moved := *src
	moved.ParentID = dstDir.GetID()
	moved.Path = ""
	return wrap(&moved), nil
}

func (d *MiCloud) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if p, ok := asPlaceholder(srcObj); ok {
		if err := xiaomidrive.ValidateName(newName); err != nil {
			return nil, err
		}
		return d.placePlaceholder(ctx, p, p.parentID, newName, false)
	}
	src := toFile(srcObj)
	// The service does not know about local empty files and would happily
	// take the name, after which the empty file would vanish from listings.
	if src.ParentID != "" && !strings.EqualFold(src.Name, newName) {
		if err := d.placeholderConflict(src.ParentID, newName); err != nil {
			return nil, err
		}
	}
	if err := d.client.Rename(ctx, src.ID, newName); err != nil {
		return nil, mapErr(err)
	}
	renamed := *src
	renamed.Name = newName
	renamed.Path = ""
	return wrap(&renamed), nil
}

func (d *MiCloud) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if p, ok := asPlaceholder(srcObj); ok {
		return d.placePlaceholder(ctx, p, dstDir.GetID(), p.Name, true)
	}
	if err := d.client.Copy(ctx, []*xiaomidrive.File{toFile(srcObj)}, dstDir.GetID()); err != nil {
		return nil, mapErr(err)
	}
	// The copy gets a new ID the task does not report; returning nil makes
	// op insert a placeholder that is refreshed on next use.
	return nil, nil
}

func (d *MiCloud) Remove(ctx context.Context, obj model.Obj) error {
	if p, ok := asPlaceholder(obj); ok {
		d.removePlaceholder(p)
		return nil
	}
	if err := d.client.Remove(ctx, []*xiaomidrive.File{toFile(obj)}); err != nil {
		return mapErr(err)
	}
	if obj.IsDir() {
		d.dropPlaceholdersIn(obj.GetID())
	}
	return nil
}

func (d *MiCloud) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	size := file.GetSize()
	if size == 0 {
		if err := xiaomidrive.ValidateName(file.GetName()); err != nil {
			return nil, err
		}
		return d.createPlaceholder(dstDir.GetID(), file.GetName(), file.ModTime(), file.CreateTime())
	}
	// Hashes of the whole file and of every 4 MiB block must be sent before
	// any data, so the stream is cached first (reported as 0-50%).
	cached, err := file.CacheFullAndWriter(&up, nil)
	if err != nil {
		return nil, err
	}
	opts := xiaomidrive.UploadOptions{
		Concurrency: d.UploadConcurrency,
		MimeType:    file.GetMimetype(),
		ModTime:     file.ModTime(),
		CreateTime:  file.CreateTime(),
		Progress: func(uploaded int64) {
			up(float64(uploaded) / float64(size) * 100)
		},
		WrapBody: func(ctx context.Context, r io.Reader) io.Reader {
			return driver.NewLimitedUploadStream(ctx, r)
		},
	}
	f, err := d.client.UploadReaderAt(ctx, dstDir.GetID(), file.GetName(), cached, size, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	// op.Put removes a same-name empty file first; this also covers a name
	// differing only in case.
	d.dropPlaceholder(dstDir.GetID(), file.GetName())
	return wrap(f), nil
}

func (d *MiCloud) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	q, err := d.client.Quota(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return &model.StorageDetails{DiskUsage: model.DiskUsage{TotalSpace: q.Total, UsedSpace: q.Used}}, nil
}

var (
	_ driver.Driver       = (*MiCloud)(nil)
	_ driver.MkdirResult  = (*MiCloud)(nil)
	_ driver.MoveResult   = (*MiCloud)(nil)
	_ driver.RenameResult = (*MiCloud)(nil)
	_ driver.CopyResult   = (*MiCloud)(nil)
	_ driver.Remove       = (*MiCloud)(nil)
	_ driver.PutResult    = (*MiCloud)(nil)
	_ driver.WithDetails  = (*MiCloud)(nil)
)
