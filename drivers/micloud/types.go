package micloud

import (
	"errors"
	"fmt"

	"github.com/Guyuepp/micloud"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// Object adapts *micloud.File to model.Obj.
type Object struct {
	*micloud.File
}

func (o *Object) GetHash() utils.HashInfo {
	if o.SHA1 == "" {
		return utils.HashInfo{}
	}
	return utils.NewHashInfo(utils.SHA1, o.SHA1)
}

var _ model.Obj = (*Object)(nil)

func wrap(f *micloud.File) *Object { return &Object{File: f} }

// toFile recovers the library object behind a model.Obj. Objects built by
// op (the root, or placeholders) only carry an ID, which is all most calls
// need.
func toFile(obj model.Obj) *micloud.File {
	if o, ok := obj.(*Object); ok {
		return o.File
	}
	if obj.IsDir() {
		return micloud.FolderRef(obj.GetID())
	}
	return &micloud.File{ID: obj.GetID(), Name: obj.GetName(), Size: obj.GetSize(), Type: micloud.TypeOther}
}

// mapErr translates library errors into the errs values op understands,
// keeping the original message.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, micloud.ErrNotFound):
		return fmt.Errorf("%w: %v", errs.ObjectNotFound, err)
	case errors.Is(err, micloud.ErrExist):
		return fmt.Errorf("%w: %v", errs.ObjectAlreadyExists, err)
	case errors.Is(err, micloud.ErrNotDir):
		return fmt.Errorf("%w: %v", errs.NotFolder, err)
	case errors.Is(err, micloud.ErrIsDir):
		return fmt.Errorf("%w: %v", errs.NotFile, err)
	}
	return err
}

// additionStore persists rotating credentials into the storage's Addition.
type additionStore struct {
	d *MiCloud
}

func (s *additionStore) Load() (*micloud.Credentials, error) {
	s.d.saveMu.Lock()
	defer s.d.saveMu.Unlock()
	a := &s.d.Addition
	return &micloud.Credentials{
		UserID:            a.UserID,
		PassToken:         a.PassToken,
		ServiceToken:      a.ServiceToken,
		DeviceID:          a.DeviceID,
		DeviceFingerprint: a.DeviceFingerprint,
	}, nil
}

func (s *additionStore) Save(c *micloud.Credentials) error {
	a := &s.d.Addition
	s.d.saveMu.Lock()
	same := a.ServiceToken == c.ServiceToken && a.PassToken == c.PassToken && a.UserID == c.UserID &&
		a.DeviceID == c.DeviceID && a.DeviceFingerprint == c.DeviceFingerprint
	s.d.saveMu.Unlock()
	if same {
		return nil
	}
	s.d.persist(func() {
		if c.UserID != "" {
			a.UserID = c.UserID
		}
		if c.PassToken != "" {
			a.PassToken = c.PassToken
		}
		a.ServiceToken = c.ServiceToken
		a.DeviceID = c.DeviceID
		a.DeviceFingerprint = c.DeviceFingerprint
	})
	return nil
}
