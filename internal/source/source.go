package source

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/inraise/pgm/internal/model"
)

type RawFile struct {
	Name    string
	Content []byte
}

func Load(fsys fs.FS, dir string) ([]RawFile, error) {
	if dir == "" {
		dir = "."
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("%w: каталог %q: %w", model.ErrSource, dir, err)
	}

	files := make([]RawFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".sql") {
			continue
		}
		content, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%w: файл %q: %w", model.ErrSource, e.Name(), err)
		}
		files = append(files, RawFile{Name: e.Name(), Content: content})
	}
	return files, nil
}
