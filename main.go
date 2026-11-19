// terraquery ingests Terraform state files, infers cross-state dependencies
// and stores them in an embedded SQLite database queryable over SQL.
//
//	terraquery [-min 0.5] [-v] [-json out.json] [name=]state.tfstate ...
//	terraquery -serve [-addr :8080] [-db terraquery.db]
//	terraquery -query "SELECT * FROM dependencies" [-db terraquery.db]
//
// A state's name defaults to its file stem (or its directory name when the
// file is called terraform.tfstate). Use name=path to set it explicitly; the
// name is also what terraform_remote_state keys are matched against.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"tfresolve/api"
	"tfresolve/db"
	"tfresolve/resolver"
	"tfresolve/state"
)

// Populated by GoReleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func parseArg(a string) (name, path string) {
	if i := strings.Index(a, "="); i > 0 {
		return a[:i], a[i+1:]
	}
	abs, err := filepath.Abs(a)
	if err != nil {
		abs = a
	}
	base := filepath.Base(abs)
	if base == "terraform.tfstate" {
		return filepath.Base(filepath.Dir(abs)), a
	}
	return strings.TrimSuffix(strings.TrimSuffix(base, ".json"), ".tfstate"), a
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "terraquery:", err)
	os.Exit(1)
}

func main() {
	min := flag.Float64("min", 0.5, "minimum confidence to report")
	verbose := flag.Bool("v", false, "print every edge, not just state-level dependencies")
	jsonOut := flag.String("json", "", "write dependencies and edges to this JSON file")
	showVersion := flag.Bool("version", false, "print version and exit")
	serve := flag.Bool("serve", false, "run the HTTP API server")
	addr := flag.String("addr", ":8080", "HTTP listen address (with -serve)")
	dbPath := flag.String("db", "terraquery.db", "SQLite file; empty disables persistence")
	query := flag.String("query", "", "run a read-only SQL query and print JSON")
	flag.Parse()

	if *showVersion {
		fmt.Printf("terraquery %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	if *serve {
		if *dbPath == "" {
			fatal(errors.New("-serve needs a -db path"))
		}
		runServer(*addr, *dbPath)
		return
	}

	if *query != "" {
		if *dbPath == "" {
			fatal(errors.New("-query needs a -db path"))
		}
		d, err := db.Open(*dbPath)
		if err != nil {
			fatal(err)
		}
		defer d.Close()
		res, err := d.Query(*query)
		if err != nil {
			fatal(err)
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return
	}

	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: terraquery [-min 0.5] [-v] [-json out.json] [-db terraquery.db] [name=]state.tfstate ...")
		fmt.Fprintln(os.Stderr, "       terraquery -serve [-addr :8080] [-db terraquery.db]")
		fmt.Fprintln(os.Stderr, "       terraquery -query \"SELECT ...\" [-db terraquery.db]")
		os.Exit(2)
	}

	var srcs []resolver.Source
	seen := map[string]bool{}
	for _, a := range flag.Args() {
		name, path := parseArg(a)
		if seen[name] {
			fatal(fmt.Errorf("duplicate state name %q; use name=path to disambiguate", name))
		}
		seen[name] = true
		f, err := state.Load(path)
		if err != nil {
			fatal(err)
		}
		srcs = append(srcs, resolver.Source{Name: name, Filename: path, File: f})
	}

	edges := resolver.Resolve(srcs)
	deps := resolver.Summarize(edges, *min)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FROM\tTO\tCONF\tEVIDENCE\tKINDS")
	for _, d := range deps {
		fmt.Fprintf(tw, "%s\t%s\t%.2f\t%d\t%s\n", d.From, d.To, d.Confidence, d.Evidence, strings.Join(d.Kinds, ","))
	}
	tw.Flush()

	if *verbose {
		fmt.Println()
		tw = tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "FROM\tFROM ADDR\tATTR\tTO\tTO ADDR\tKIND\tCONF\tMATCHED")
		for _, e := range edges {
			if e.Confidence < *min {
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%.2f\t%s\n",
				e.From, e.FromAddr, e.FromAttr, e.To, e.ToAddr, e.Kind, e.Confidence, e.Matched)
		}
		tw.Flush()
	}

	if *jsonOut != "" {
		b, _ := json.MarshalIndent(map[string]any{"dependencies": deps, "edges": edges}, "", "  ")
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fatal(err)
		}
	}

	if *dbPath != "" {
		d, err := db.Open(*dbPath)
		if err != nil {
			fatal(err)
		}
		defer d.Close()
		if err := d.Ingest(srcs); err != nil {
			fatal(err)
		}
	}
}

func runServer(addr, dbPath string) {
	d, err := db.Open(dbPath)
	if err != nil {
		fatal(err)
	}
	defer d.Close()

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.New(d).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("terraquery listening on %s (db %s)", addr, d.Path())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}
