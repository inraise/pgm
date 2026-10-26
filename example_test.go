package pgm_test

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/inraise/pgm"
)

//go:embed testdata/migrations/*.sql
var migrationsFS embed.FS

func ExampleNew() {
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	m, err := pgm.New(db, migrationsFS, "testdata/migrations",
		pgm.WithLockTimeout(time.Minute))
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func ExampleMigrator_Up() {
	var m *pgm.Migrator

	err := m.Up(context.Background())

	var me *pgm.MigrationError
	switch {
	case err == nil:
		fmt.Println("схема актуальна")
	case errors.Is(err, pgm.ErrChecksumMismatch):
		fmt.Println("файл применённой миграции изменили:", err)
	case errors.Is(err, pgm.ErrLockTimeout):
		fmt.Println("другой экземпляр выполняет миграции")
	case errors.As(err, &me):
		fmt.Printf("ошибка в миграции %s: %v\n", me.Migration.Label(), me.Err)
	default:
		fmt.Println("ошибка:", err)
	}
}

func ExampleMigrator_Status() {
	var m *pgm.Migrator

	items, err := m.Status(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, it := range items {
		fmt.Printf("%04d  %-20s  %s\n", it.Version, it.Name, it.State)
	}
}

func ExampleWithObserver() {
	var db *sql.DB

	m, err := pgm.New(db, migrationsFS, "testdata/migrations",
		pgm.WithObserver(func(e pgm.Event) {
			if e.Kind == pgm.EventDone {
				fmt.Printf("%s выполнена за %s\n", e.Migration.Label(), e.Duration)
			}
		}))
	if err != nil {
		log.Fatal(err)
	}
	_ = m
}
