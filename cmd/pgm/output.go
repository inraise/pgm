package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/inraise/pgm"
)

type planJSON struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
}

type doneJSON struct {
	Version    int64  `json:"version"`
	Name       string `json:"name"`
	DurationMs int64  `json:"duration_ms"`
}

type statusJSON struct {
	Version   int64      `json:"version"`
	Name      string     `json:"name"`
	State     string     `json:"state"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
}

func (a *app) writeJSON(v any) error { return a.writeJSONTo(a.stdout, v) }

func (a *app) writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (a *app) printPlan(plan []pgm.Migration, heading, empty string) error {
	if a.o.jsonOut {
		items := make([]planJSON, 0, len(plan))
		for _, m := range plan {
			items = append(items, planJSON{m.Version, m.Name})
		}
		return a.writeJSON(map[string]any{"dry_run": true, "plan": items})
	}
	if len(plan) == 0 {
		_, err := fmt.Fprintln(a.stdout, empty)
		return err
	}
	fmt.Fprintln(a.stdout, heading)
	for _, m := range plan {
		fmt.Fprintf(a.stdout, "  %s\n", m.Label())
	}
	return nil
}

func (a *app) printResult(ctx context.Context, m *pgm.Migrator, key, label, empty string) error {
	if a.o.quiet {
		return nil
	}
	ver, ok := a.schemaVersion(ctx, m)
	if a.o.jsonOut {
		items := make([]doneJSON, 0, len(a.done))
		for _, e := range a.done {
			items = append(items, doneJSON{e.Migration.Version, e.Migration.Name, e.Duration.Milliseconds()})
		}
		out := map[string]any{key: items}
		if ok {
			out["version"] = ver
		}
		return a.writeJSON(out)
	}
	line := empty
	if n := len(a.done); n > 0 {
		line = fmt.Sprintf("%s: %d.", label, n)
	}
	if ok {
		line += fmt.Sprintf(" Версия схемы: %d", ver)
	}
	_, err := fmt.Fprintln(a.stdout, line)
	return err
}

func (a *app) printStatus(items []pgm.StatusItem) error {
	ver := maxApplied(items)
	if a.o.jsonOut {
		list := make([]statusJSON, 0, len(items))
		for _, it := range items {
			s := statusJSON{Version: it.Version, Name: it.Name, State: string(it.State)}
			if !it.AppliedAt.IsZero() {
				t := it.AppliedAt.UTC()
				s.AppliedAt = &t
			}
			list = append(list, s)
		}
		return a.writeJSON(map[string]any{"version": ver, "migrations": list})
	}

	fmt.Fprintf(a.stdout, "Версия схемы: %d\n\n", ver)
	if len(items) == 0 {
		_, err := fmt.Fprintln(a.stdout, "Миграций нет.")
		return err
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Версия\tНазвание\tСостояние\tПрименена")
	for _, it := range items {
		applied := "-"
		if !it.AppliedAt.IsZero() {
			applied = it.AppliedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%04d\t%s\t%s\t%s\n", it.Version, it.Name, it.State, applied)
	}
	return tw.Flush()
}

func (a *app) logf(format string, args ...any) {
	fmt.Fprintf(a.stderr, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

type verboseHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	attrs []slog.Attr
}

func newVerboseHandler(w io.Writer) *verboseHandler {
	return &verboseHandler{w: w, mu: &sync.Mutex{}}
}

func (h *verboseHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *verboseHandler) Handle(_ context.Context, r slog.Record) error {
	var parts []string
	add := func(at slog.Attr) bool {
		parts = append(parts, at.Key+"="+at.Value.String())
		return true
	}
	for _, at := range h.attrs {
		add(at)
	}
	r.Attrs(add)

	line := r.Message
	if len(parts) > 0 {
		line += ": " + strings.Join(parts, " ")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintf(h.w, "[%s] %s\n", r.Time.Format("15:04:05"), line)
	return err
}

func (h *verboseHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &c
}

func (h *verboseHandler) WithGroup(string) slog.Handler {
	return h
}
