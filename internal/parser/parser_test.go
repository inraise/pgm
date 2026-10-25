package parser

import (
	"errors"
	"testing"

	"github.com/inraise/pgm/internal/model"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		content  string
		wantVer  int64
		wantName string
		wantUp   string
		wantDown string
		wantErr  bool
	}{
		{"up и down", "0001_create_users.sql",
			"-- +up\nCREATE TABLE users (id int);\n-- +down\nDROP TABLE users;\n",
			1, "create_users", "CREATE TABLE users (id int);", "DROP TABLE users;", false},
		{"только up", "0002_add_index.sql",
			"-- +up\nCREATE INDEX i ON t(a);\n",
			2, "add_index", "CREATE INDEX i ON t(a);", "", false},
		{"комментарий перед up", "0003_x.sql",
			"-- автор\n\n-- +up\nSELECT 1;\n",
			3, "x", "SELECT 1;", "", false},
		{"CRLF и BOM", "0004_crlf.sql",
			"\xef\xbb\xbf-- +up\r\nSELECT 1;\r\n-- +down\r\nSELECT 2;\r\n",
			4, "crlf", "SELECT 1;", "SELECT 2;", false},
		{"версия из пяти цифр", "00010_big.sql",
			"-- +up\nSELECT 1;\n",
			10, "big", "SELECT 1;", "", false},

		{"имя: короткая версия", "1_short.sql", "-- +up\nSELECT 1;\n", 0, "", "", "", true},
		{"имя: верхний регистр", "0001_Upper.sql", "-- +up\nSELECT 1;\n", 0, "", "", "", true},
		{"имя: дефис вместо _", "0001-name.sql", "-- +up\nSELECT 1;\n", 0, "", "", "", true},
		{"имя: не .sql", "0001_name.txt", "-- +up\nSELECT 1;\n", 0, "", "", "", true},
		{"версия ноль", "0000_zero.sql", "-- +up\nSELECT 1;\n", 0, "", "", "", true},
		{"нет секции up", "0005_none.sql", "SELECT 1;\n", 0, "", "", "", true},
		{"текст до up", "0006_before.sql", "SELECT 1;\n-- +up\nSELECT 2;\n", 0, "", "", "", true},
		{"up повторно", "0007_dup.sql", "-- +up\nSELECT 1;\n-- +up\nSELECT 2;\n", 0, "", "", "", true},
		{"down раньше up", "0008_order.sql", "-- +down\nSELECT 1;\n-- +up\nSELECT 2;\n", 0, "", "", "", true},
		{"пустая up", "0009_empty_up.sql", "-- +up\n\n-- +down\nSELECT 1;\n", 0, "", "", "", true},
		{"пустая down", "0010_empty_down.sql", "-- +up\nSELECT 1;\n-- +down\n\n", 0, "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.fileName, []byte(tt.content))
			if tt.wantErr {
				if !errors.Is(err, model.ErrInvalidSet) {
					t.Fatalf("ожидалась ErrInvalidSet, получено: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if got.Version != tt.wantVer || got.Name != tt.wantName ||
				got.Up != tt.wantUp || got.Down != tt.wantDown {
				t.Errorf("получено %+v", got)
			}
		})
	}
}

func TestParseChecksum(t *testing.T) {
	a, err := Parse("0001_a.sql", []byte("-- +up\nSELECT 1;\n"))
	if err != nil {
		t.Fatal(err)
	}
	crlf, err := Parse("0001_a.sql", []byte("-- +up\r\nSELECT 1;\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := Parse("0001_a.sql", []byte("-- +up\nSELECT 2;\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Checksum) != 64 {
		t.Errorf("длина контрольной суммы %d, ожидалось 64", len(a.Checksum))
	}
	if a.Checksum != crlf.Checksum {
		t.Error("CRLF не должен менять контрольную сумму")
	}
	if a.Checksum == other.Checksum {
		t.Error("разное содержимое дало одинаковую контрольную сумму")
	}
}

func mk(versions ...int64) []model.Migration {
	set := make([]model.Migration, len(versions))
	for i, v := range versions {
		set[i] = model.Migration{Version: v}
	}
	return set
}

func TestValidateSet(t *testing.T) {
	tests := []struct {
		name    string
		set     []model.Migration
		wantErr bool
	}{
		{"пустой набор", mk(), false},
		{"по порядку", mk(1, 2, 3), false},
		{"не по порядку", mk(3, 1, 2), false},
		{"пропуск", mk(1, 3), true},
		{"не с единицы", mk(2, 3), true},
		{"дубль", mk(1, 1, 2), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSet(tt.set)
			if tt.wantErr != errors.Is(err, model.ErrInvalidSet) {
				t.Errorf("wantErr=%v, получено: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateSetKeepsInputOrder(t *testing.T) {
	set := mk(3, 1, 2)
	_ = ValidateSet(set)
	if set[0].Version != 3 || set[1].Version != 1 || set[2].Version != 2 {
		t.Error("ValidateSet изменил порядок входного среза")
	}
}
