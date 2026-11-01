package micloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Guyuepp/xiaomi-drive-go"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// The service rejects 0-byte files ("invalid size"), but clients create them
// all the time: touch, editors' lock files, and macOS Finder, which PUTs an
// empty placeholder before the real content. Empty files are therefore kept
// locally, persisted in the storage addition, and shown in listings. Writing
// content later goes through op.Put, which removes an existing 0-byte file
// first, so the placeholder is dropped and the real file uploaded.

// maxPlaceholders bounds the persisted set so a misbehaving client cannot
// grow the storage record without limit.
const maxPlaceholders = 10000

const placeholderIDPrefix = "placeholder:"

type placeholder struct {
	Name     string `json:"n"`
	Modified int64  `json:"m"`
	Created  int64  `json:"c"`
}

// placeholderObj is an empty file that exists only locally.
type placeholderObj struct {
	model.Object
	parentID string
}

func (*placeholderObj) IsKnownEmpty() bool { return true }

func (d *MiCloud) newPlaceholderObj(parentID string, p placeholder) *placeholderObj {
	return &placeholderObj{
		Object: model.Object{
			ID:       placeholderIDPrefix + parentID + "/" + p.Name,
			Name:     p.Name,
			Modified: time.UnixMilli(p.Modified),
			Ctime:    time.UnixMilli(p.Created),
		},
		parentID: parentID,
	}
}

func asPlaceholder(obj model.Obj) (*placeholderObj, bool) {
	p, ok := obj.(*placeholderObj)
	return p, ok
}

func phKey(name string) string { return strings.ToLower(name) }

// loadPlaceholders restores the set from the addition. Called from Init.
func (d *MiCloud) loadPlaceholders() {
	d.phMu.Lock()
	defer d.phMu.Unlock()
	d.ph = map[string]map[string]placeholder{}
	if d.Placeholders == "" {
		return
	}
	var stored map[string][]placeholder
	if err := json.Unmarshal([]byte(d.Placeholders), &stored); err != nil {
		return
	}
	for parent, list := range stored {
		for _, p := range list {
			d.phAddLocked(parent, p)
		}
	}
}

func (d *MiCloud) phCountLocked() int {
	n := 0
	for _, m := range d.ph {
		n += len(m)
	}
	return n
}

func (d *MiCloud) phAddLocked(parentID string, p placeholder) {
	m := d.ph[parentID]
	if m == nil {
		m = map[string]placeholder{}
		d.ph[parentID] = m
	}
	m[phKey(p.Name)] = p
}

func (d *MiCloud) phDeleteLocked(parentID, name string) bool {
	m := d.ph[parentID]
	if _, ok := m[phKey(name)]; !ok {
		return false
	}
	delete(m, phKey(name))
	if len(m) == 0 {
		delete(d.ph, parentID)
	}
	return true
}

// phPersistLocked writes the set back into the addition and saves it.
func (d *MiCloud) phPersistLocked() {
	stored := make(map[string][]placeholder, len(d.ph))
	for parent, m := range d.ph {
		for _, p := range m {
			stored[parent] = append(stored[parent], p)
		}
	}
	b, _ := json.Marshal(stored)
	d.persist(func() { d.Placeholders = string(b) })
}

func (d *MiCloud) phExistsLocked(parentID, name string) bool {
	_, ok := d.ph[parentID][phKey(name)]
	return ok
}

// createPlaceholder records an empty file under parentID.
func (d *MiCloud) createPlaceholder(parentID, name string, mod, created time.Time) (model.Obj, error) {
	d.phMu.Lock()
	defer d.phMu.Unlock()
	if !d.phExistsLocked(parentID, name) && d.phCountLocked() >= maxPlaceholders {
		return nil, fmt.Errorf("too many empty files kept locally (%d); the service cannot store empty files", maxPlaceholders)
	}
	now := time.Now()
	if mod.IsZero() {
		mod = now
	}
	if created.IsZero() {
		created = mod
	}
	p := placeholder{Name: name, Modified: mod.UnixMilli(), Created: created.UnixMilli()}
	d.phAddLocked(parentID, p)
	d.phPersistLocked()
	return d.newPlaceholderObj(parentID, p), nil
}

// mergePlaceholders appends the placeholders of dirID to a server listing.
// A placeholder whose name now exists on the server (uploaded elsewhere) is
// dropped: the server copy wins.
func (d *MiCloud) mergePlaceholders(dirID string, objs []model.Obj) []model.Obj {
	d.phMu.Lock()
	defer d.phMu.Unlock()
	m := d.ph[dirID]
	if len(m) == 0 {
		return objs
	}
	onServer := make(map[string]bool, len(objs))
	for _, o := range objs {
		onServer[phKey(o.GetName())] = true
	}
	changed := false
	for key, p := range m {
		if onServer[key] {
			delete(m, key)
			changed = true
			continue
		}
		objs = append(objs, d.newPlaceholderObj(dirID, p))
	}
	if len(m) == 0 {
		delete(d.ph, dirID)
	}
	if changed {
		d.phPersistLocked()
	}
	return objs
}

func (d *MiCloud) removePlaceholder(p *placeholderObj) {
	d.phMu.Lock()
	defer d.phMu.Unlock()
	if d.phDeleteLocked(p.parentID, p.Name) {
		d.phPersistLocked()
	}
}

// dropPlaceholder removes a placeholder by location, e.g. after real content
// was uploaded under the same name.
func (d *MiCloud) dropPlaceholder(parentID, name string) {
	d.phMu.Lock()
	defer d.phMu.Unlock()
	if d.phDeleteLocked(parentID, name) {
		d.phPersistLocked()
	}
}

// dropPlaceholdersIn forgets the placeholders directly inside a removed
// folder. Placeholders in deeper subfolders are not tracked by ancestry and
// stay in the persisted set, unreachable but harmless.
func (d *MiCloud) dropPlaceholdersIn(folderID string) {
	d.phMu.Lock()
	defer d.phMu.Unlock()
	if _, ok := d.ph[folderID]; ok {
		delete(d.ph, folderID)
		d.phPersistLocked()
	}
}

// placePlaceholder adds a placeholder at dstID with name, failing if the name
// is taken by a server item or another placeholder.
func (d *MiCloud) placePlaceholder(ctx context.Context, src *placeholderObj, dstID, name string, keepSource bool) (model.Obj, error) {
	children, err := d.client.List(ctx, xiaomidrive.FolderRef(dstID))
	if err != nil {
		return nil, mapErr(err)
	}
	for _, c := range children {
		if strings.EqualFold(c.Name, name) {
			return nil, fmt.Errorf("%w: %q", errs.ObjectAlreadyExists, c.Name)
		}
	}
	d.phMu.Lock()
	defer d.phMu.Unlock()
	sameSlot := src.parentID == dstID && phKey(src.Name) == phKey(name)
	if !sameSlot && d.phExistsLocked(dstID, name) {
		return nil, fmt.Errorf("%w: %q", errs.ObjectAlreadyExists, name)
	}
	p := placeholder{Name: name, Modified: src.Modified.UnixMilli(), Created: src.Ctime.UnixMilli()}
	if !keepSource {
		d.phDeleteLocked(src.parentID, src.Name)
	} else if d.phCountLocked() >= maxPlaceholders {
		return nil, fmt.Errorf("too many empty files kept locally (%d)", maxPlaceholders)
	}
	d.phAddLocked(dstID, p)
	d.phPersistLocked()
	return d.newPlaceholderObj(dstID, p), nil
}

// emptyReader serves the content of a placeholder.
type emptyReader struct{}

func (emptyReader) RangeRead(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func placeholderLink() *model.Link {
	return &model.Link{RangeReader: emptyReader{}, ContentLength: 0}
}

// errPlaceholderRedirect is returned when a client asks for a direct link to
// a local empty file: there is no URL to redirect to.
var errPlaceholderRedirect = fmt.Errorf("%w: empty file exists only locally and has no direct link; read it through the proxy", errs.NotSupport)
