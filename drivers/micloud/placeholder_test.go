package micloud

import (
	"context"
	"io"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

func TestPlaceholderDeclaresEmptyContent(t *testing.T) {
	d := &MiCloud{}
	p := d.newPlaceholderObj("0", placeholder{Name: "empty.txt"})
	var file model.Obj = p
	known, ok := file.(model.ObjKnownEmpty)
	if !ok || !known.IsKnownEmpty() || file.GetSize() != 0 || file.IsDir() {
		t.Fatal("placeholder did not explicitly declare empty content")
	}
	link, err := d.Link(context.Background(), file, model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := link.RangeReader.RangeRead(context.Background(), http_range.Range{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil || len(b) != 0 {
		t.Fatalf("placeholder body=%q error=%v", b, err)
	}
	if _, err := d.Link(context.Background(), file, model.LinkArgs{Redirect: true}); err == nil {
		t.Fatal("direct-link request returned an empty redirect URL")
	}
}
