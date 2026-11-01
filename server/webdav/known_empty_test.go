package webdav

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type knownEmptyObject struct{ model.Object }

func (*knownEmptyObject) IsKnownEmpty() bool { return true }

func TestKnownEmptyContent(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		var file model.Obj = &knownEmptyObject{Object: model.Object{Name: "empty.txt"}}
		if wrapped {
			file = &model.ObjStorageDetails{Obj: &model.ObjWrapName{Name: "wrapped.txt", Obj: file}}
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "http://localhost/dav/empty.txt", nil)
			if !serveKnownEmpty(w, r, file) || w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Location") != "" || w.Header().Get("Content-Length") != "0" {
				t.Fatalf("method=%s wrapped=%v: response=%+v", method, wrapped, w.Result())
			}
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://localhost/dav/empty.txt", nil)
		r.Header.Set("Range", "bytes=0-1")
		// ServeContent ignores a non-overlapping range for a zero-byte file.
		if !serveKnownEmpty(w, r, file) || w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "0" {
			t.Fatalf("range response=%+v", w.Result())
		}
	}
}

func TestUnknownSizeIsNotEmptyContent(t *testing.T) {
	for _, file := range []model.Obj{
		&model.Object{Name: "unknown-size.txt"},
		&model.Object{Name: "nonempty.txt", Size: 10},
		&knownEmptyObject{Object: model.Object{Name: "inconsistent.txt", Size: 10}},
		&knownEmptyObject{Object: model.Object{Name: "folder", IsFolder: true}},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://localhost/dav/file", nil)
		if serveKnownEmpty(w, r, file) {
			t.Fatalf("object incorrectly handled as empty: %+v", file)
		}
		if len(w.Header()) != 0 || w.Body.Len() != 0 {
			t.Fatal("fallthrough wrote a response")
		}
	}
}
