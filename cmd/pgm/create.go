package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/inraise/pgm"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9]+(?:_[a-z0-9]+)*$`)
	versionRe = regexp.MustCompile(`^(\d+)_`)
)

func (a *app) runCreate(name string) error {
	path, err := createMigrationFile(a.o.dir, name)
	if err != nil {
		return err
	}
	if a.o.quiet {
		return nil
	}
	if a.o.jsonOut {
		return a.writeJSON(map[string]string{"created": path})
	}
	_, err = fmt.Fprintf(a.stdout, "Создан файл %s\n", path)
	return err
}

func createMigrationFile(dir, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf(
			"%w: имя миграции %q: допустимы строчные латинские буквы и цифры, слова разделяются _",
			pgm.ErrInvalidArgument, name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("создание каталога %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("чтение каталога %s: %w", dir, err)
	}
	var last int64
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".sql") {
			continue
		}
		if m := versionRe.FindStringSubmatch(e.Name()); m != nil {
			if v, err := strconv.ParseInt(m[1], 10, 64); err == nil && v > last {
				last = v
			}
		}
	}

	path := filepath.Join(dir, fmt.Sprintf("%04d_%s.sql", last+1, name))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("создание файла %s: %w", path, err)
	}
	_, werr := f.WriteString(fmt.Sprintf("-- Миграция: %s\n-- +up\n\n\n-- +down\n\n", name))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("запись файла %s: %w", path, werr)
	}
	return path, nil
}
