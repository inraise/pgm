package source

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/inraise/pgm/internal/model"
)

//go:embed testdata/migrations
var embedded embed.FS

func names(files []RawFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Name
	}
	return out
}

func TestLoadFilters(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0002_b.sql":     {Data: []byte("b")},
		"migrations/0001_a.sql":     {Data: []byte("a")},
		"migrations/README.md":      {Data: []byte("игнорируется")},
		"migrations/sub/0003_c.sql": {Data: []byte("c")},
		"migrations/0004_d.SQL":     {Data: []byte("d")},
	}
	got, err := Load(fsys, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0001_a.sql", "0002_b.sql", "0004_d.SQL"}
	if !slices.Equal(names(got), want) {
		t.Fatalf("файлы: %v, ожидалось %v", names(got), want)
	}
	if string(got[0].Content) != "a" || string(got[1].Content) != "b" {
		t.Errorf("содержимое прочитано неверно: %q, %q", got[0].Content, got[1].Content)
	}
}

func TestLoadRootDir(t *testing.T) {
	fsys := fstest.MapFS{"0001_a.sql": {Data: []byte("a")}}
	got, err := Load(fsys, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("получено %v, ошибка %v", names(got), err)
	}
}

func TestLoadEmptyDir(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations":           {Mode: fs.ModeDir},
		"migrations/README.md": {Data: []byte("x")},
	}
	got, err := Load(fsys, "migrations")
	if err != nil {
		t.Fatalf("пустой набор не должен быть ошибкой: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ожидался пустой результат, получено %v", names(got))
	}
}

func TestLoadMissingDir(t *testing.T) {
	_, err := Load(fstest.MapFS{}, "nope")
	if !errors.Is(err, model.ErrSource) {
		t.Errorf("ожидалась ErrSource, получено: %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("исходная ошибка должна сохраняться: %v", err)
	}
}

func TestLoadRealFileSystems(t *testing.T) {
	tests := []struct {
		name string
		fsys fs.FS
		dir  string
	}{
		{"embed.FS", embedded, "testdata/migrations"},
		{"os.DirFS", os.DirFS("testdata"), "migrations"},
	}
	want := []string{"0001_init.sql", "0002_add_name.sql"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(tt.fsys, tt.dir)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(names(got), want) {
				t.Errorf("файлы: %v, ожидалось %v", names(got), want)
			}
		})
	}
}
