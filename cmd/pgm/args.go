package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
)

const usageText = `pgm - управление миграциями PostgreSQL

Использование:
  pgm [флаги] <команда>

Команды:
  up            применить все ожидающие миграции
  down N        откатить последние N миграций (нужен --yes)
  status        показать состояние миграций
  create имя    создать файл миграции

Флаги:
  --dsn строка        строка подключения (или переменная PGM_DSN)
  --dir путь          каталог миграций (по умолчанию ./migrations)
  --dry-run           показать план, ничего не выполняя
  --yes               подтвердить откат
  --lock-timeout сек  ожидание блокировки (по умолчанию 30)
  --quiet             выводить только ошибки
  --verbose           подробный вывод
  --json              вывод в формате JSON
  --version           показать версию
`

type options struct {
	dsn         string
	dir         string
	dryRun      bool
	yes         bool
	quiet       bool
	verbose     bool
	jsonOut     bool
	lockTimeout int
	showVersion bool
}

type command struct {
	name string
	n    int
	arg  string
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

func parseArgs(args []string, getenv func(string) string) (options, command, error) {
	o := options{dir: "./migrations", lockTimeout: 30}
	fs := flag.NewFlagSet("pgm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.dsn, "dsn", "", "")
	fs.StringVar(&o.dir, "dir", o.dir, "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.yes, "yes", false, "")
	fs.IntVar(&o.lockTimeout, "lock-timeout", o.lockTimeout, "")
	fs.BoolVar(&o.quiet, "quiet", false, "")
	fs.BoolVar(&o.verbose, "verbose", false, "")
	fs.BoolVar(&o.jsonOut, "json", false, "")
	fs.BoolVar(&o.showVersion, "version", false, "")

	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return o, command{}, err
			}
			return o, command{}, usagef("%v", err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	if o.dsn == "" {
		o.dsn = getenv("PGM_DSN")
	}
	if o.showVersion {
		return o, command{name: "version"}, nil
	}
	if len(pos) == 0 {
		return o, command{}, usagef("не указана команда")
	}
	if o.quiet && o.verbose {
		return o, command{}, usagef("флаги --quiet и --verbose несовместимы")
	}
	if o.lockTimeout < 1 {
		return o, command{}, usagef("--lock-timeout должен быть не меньше 1 секунды")
	}

	c := command{name: pos[0]}
	extra := pos[1:]
	switch c.name {
	case "help":
		return o, c, flag.ErrHelp
	case "up", "status":
		if len(extra) != 0 {
			return o, c, usagef("команда %s не принимает аргументов", c.name)
		}
	case "down":
		if len(extra) != 1 {
			return o, c, usagef("команда down требует число миграций: pgm down N")
		}
		n, err := strconv.Atoi(extra[0])
		if err != nil || n < 1 {
			return o, c, usagef("число миграций для отката должно быть целым и не меньше 1, получено %q", extra[0])
		}
		c.n = n
	case "create":
		if len(extra) != 1 {
			return o, c, usagef("команда create требует имя миграции: pgm create имя")
		}
		c.arg = extra[0]
	default:
		return o, c, usagef("неизвестная команда %q", c.name)
	}
	if o.dryRun && c.name != "up" && c.name != "down" {
		return o, c, usagef("флаг --dry-run применим только к командам up и down")
	}
	return o, c, nil
}
