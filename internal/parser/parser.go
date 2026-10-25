package parser

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/inraise/pgm/internal/model"
)

var fileNameRe = regexp.MustCompile(`^(\d{4,})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

const (
	markerUp   = "-- +up"
	markerDown = "-- +down"
)

func Parse(fileName string, content []byte) (model.Migration, error) {
	match := fileNameRe.FindStringSubmatch(fileName)
	if match == nil {
		return model.Migration{}, fmt.Errorf(
			"%w: имя файла %q не соответствует формату NNNN_name.sql", model.ErrInvalidSet, fileName)
	}

	version, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || version < 1 {
		return model.Migration{}, fmt.Errorf(
			"%w: недопустимая версия в имени файла %q", model.ErrInvalidSet, fileName)
	}

	text := normalize(content)
	up, down, err := splitSections(text)
	if err != nil {
		return model.Migration{}, fmt.Errorf("%w: файл %q: %v", model.ErrInvalidSet, fileName, err)
	}

	sum := sha256.Sum256([]byte(text))
	return model.Migration{
		Version:  version,
		Name:     match[2],
		Up:       up,
		Down:     down,
		Checksum: hex.EncodeToString(sum[:]),
	}, nil
}

func normalize(content []byte) string {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))

	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func splitSections(text string) (up, down string, err error) {
	var (
		upBuf, downBuf   strings.Builder
		current          *strings.Builder
		seenUp, seenDown bool
	)

	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case markerUp:
			if seenUp {
				return "", "", fmt.Errorf("строка %d: секция up указана повторно", i+1)
			}

			seenUp = true
			current = &upBuf
			continue
		case markerDown:
			if seenDown {
				return "", "", fmt.Errorf("строка %d: секция down указана повторно", i+1)
			}

			if !seenUp {
				return "", "", fmt.Errorf("строка %d: секция down стоит раньше up", i+1)
			}

			seenDown = true
			current = &downBuf
			continue
		}
		if current == nil {
			if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
				return "", "", fmt.Errorf("строка %d: текст до секции up", i+1)
			}

			continue
		}

		current.WriteString(line)
		current.WriteByte('\n')
	}

	if !seenUp {
		return "", "", fmt.Errorf("нет секции %s", markerUp)
	}

	up = strings.TrimSpace(upBuf.String())
	if up == "" {
		return "", "", fmt.Errorf("секция up пуста")
	}

	down = strings.TrimSpace(downBuf.String())
	if seenDown && down == "" {
		return "", "", fmt.Errorf("секция down пуста (уберите её, если откат не нужен)")
	}

	return up, down, nil
}

func ValidateSet(set []model.Migration) error {
	sorted := make([]model.Migration, len(set))
	copy(sorted, set)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })

	for i, m := range sorted {
		want := int64(i + 1)
		if m.Version == want {
			continue
		}

		if i > 0 && m.Version == sorted[i-1].Version {
			return fmt.Errorf("%w: версия %d встречается дважды", model.ErrInvalidSet, m.Version)
		}

		return fmt.Errorf("%w: ожидалась версия %d, найдена %d (пропуск в нумерации)",
			model.ErrInvalidSet, want, m.Version)
	}
	return nil
}
