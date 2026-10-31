package micloud

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	UserID            string `json:"user_id" required:"true" help:"Run 'micloud login qr' then 'micloud export' to get user_id and pass_token"`
	PassToken         string `json:"pass_token" type:"text" required:"true" help:"Long-lived login credential (about 30 days, extended on use). Keep it secret"`
	UploadConcurrency int    `json:"upload_concurrency" type:"number" default:"4" help:"Number of 4 MiB blocks uploaded in parallel"`

	// Not shown in the form; persisted so a restart can reuse a fresh token.
	ServiceToken      string
	DeviceID          string
	DeviceFingerprint string
}

var config = driver.Config{
	Name:        "MiCloud",
	DefaultRoot: "0",
	// Server-side NAME ordering has inconsistent reverse semantics; let
	// OpenList sort.
	LocalSort: true,
	// The service has no in-place overwrite and auto-renames same-name
	// uploads to "name(1).ext"; let op rename the old file aside first.
	NoOverwriteUpload: true,
	CheckStatus:       true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &MiCloud{}
	})
}
