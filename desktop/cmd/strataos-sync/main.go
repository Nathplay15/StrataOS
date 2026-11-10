// StrataOS Sync keeps folders of a computer synchronized with folders of a
// StrataOS NAS. The desktop app (desktop/app) runs it as its sync engine
// ("agent", agent.go); without a command it starts that app when installed
// next to it; the other commands run it headless:
//
//	strataos-sync [--config DIR] [--account ID] pair --server URL --code CODE [--fingerprint FP | --trust]
//	strataos-sync [--config DIR] pair --link 'strataos://pair?…'
//	strataos-sync [--config DIR] pair --server URL --user NAME --password-file FILE [--otp CODE]
//	strataos-sync [--config DIR] add --local DIR --remote /srv/strata/volume/Share[/sub] [--exclude a,b]
//	strataos-sync [--config DIR] remove ID | list | status | unpair
//	strataos-sync [--config DIR] sync-once [--confirm-deletes | --restore-deletes]
//	strataos-sync [--config DIR] daemon
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"strataos-sync/internal/agent"
	"strataos-sync/internal/api"
	"strataos-sync/internal/app"
	"strataos-sync/internal/config"
)

var version = "dev"

func main() {
	attachConsole()
	global := flag.NewFlagSet("strataos-sync", flag.ExitOnError)
	dir := global.String("config", "", "folder of the settings and state (default: the user's configuration folder)")
	background := global.Bool("background", false, "start the desktop app hidden (start at login)")
	account := global.String("account", "", "account of another NAS paired by the desktop app (see accounts)")
	showVersion := global.Bool("version", false, "print the version")
	global.Usage = usage
	_ = global.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("strataos-sync", version)
		return
	}
	if *dir == "" {
		*dir = config.DefaultDir()
	}
	args := global.Args()
	if len(args) > 0 && args[0] == "agent" {
		runAgent(*dir, args[1:])
		return
	}
	if len(args) > 0 && args[0] == "accounts" {
		listAccounts(*dir)
		return
	}
	if len(args) == 0 {
		launchDesktopApp(*background)
		return
	}
	store := &config.Store{Dir: *dir}
	if *account != "" && *account != agent.MainAccount {
		if !validAccount(*account) {
			fmt.Fprintln(os.Stderr, "strataos-sync: invalid account")
			os.Exit(2)
		}
		store.Dir = filepath.Join(*dir, "accounts", *account)
	}
	if err := runCommand(store, args[0], args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "strataos-sync:", err)
		var ce exitCode
		if errors.As(err, &ce) {
			os.Exit(int(ce))
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `StrataOS Sync `+version+`: two-way synchronization of folders with a StrataOS NAS.

Usage: strataos-sync [--config DIR] [--account ID] [command]

Without a command: starts the StrataOS desktop app when it is installed.
--account: another NAS paired by the desktop app (the first one by default).
Commands (headless):
  pair      --server URL (--code CODE | --user NAME --password-file FILE [--otp CODE]) [--fingerprint FP | --trust] [--name NAME]
            or --link 'strataos://pair?…' (copied from the NAS web interface)
  add       --local DIR --remote /srv/strata/VOLUME/SHARE[/SUBFOLDER] [--exclude sub1,sub2]
            [--encrypt] [--passphrase-file FILE | --recovery-key-file FILE]: end-to-end encrypted
            folder (--encrypt makes an empty NAS folder encrypted; the passphrase may also come
            from STRATAOS_SYNC_PASSPHRASE)
  recovery-key ID  prints the recovery key of an encrypted folder
  passphrase --passphrase-file FILE ID  gives an encrypted folder a new passphrase
  remove    ID
  list      the synchronized folders
  status    the state of each folder
  sync-once synchronize until up to date, then exit [--confirm-deletes | --restore-deletes]
  daemon    synchronize continuously (Ctrl+C stops)
  unpair    sign this computer out of the NAS
  accounts  the NAS paired by the desktop app, with their account IDs
  agent     the sync engine of the desktop app (started by the app itself)
`)
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func runCommand(store *config.Store, cmd string, args []string) error {
	a, err := app.New(store, version)
	if err != nil {
		return err
	}
	a.Manual = cmd != "daemon" // sync-once, add…: no rounds in the background
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "pair":
		server := fs.String("server", "", "address of the NAS (https://nas.local:7443)")
		link := fs.String("link", "", "pairing link from the web interface")
		code := fs.String("code", "", "one-time pairing code from the web interface")
		user := fs.String("user", "", "user name (instead of a code)")
		passFile := fs.String("password-file", "", "file holding the password (- for standard input)")
		pass := fs.String("password", "", "password (visible in the process list: prefer --password-file)")
		otp := fs.String("otp", "", "two-factor code")
		fp := fs.String("fingerprint", "", "expected SHA-256 fingerprint of the NAS certificate")
		trust := fs.Bool("trust", false, "accept the certificate presented (prints its fingerprint)")
		name := fs.String("name", "", "name of this computer on the NAS")
		_ = fs.Parse(args)
		in := app.PairInput{URL: *server, Link: *link, Code: *code, Username: *user, Password: *pass, OTP: *otp, Fingerprint: *fp, DeviceName: *name}
		if *passFile != "" {
			var b []byte
			if *passFile == "-" {
				b, err = io.ReadAll(os.Stdin)
			} else {
				b, err = os.ReadFile(*passFile)
			}
			if err != nil {
				return err
			}
			in.Password = strings.TrimRight(string(b), "\r\n")
		}
		if in.Link != "" {
			base, lfp, lcode, _, err := app.ParseLink(in.Link)
			if err != nil {
				return err
			}
			in.URL, in.Code, in.Link = base, lcode, ""
			if lfp != "" {
				in.Fingerprint = lfp
			}
		}
		base, err := app.NormalizeURL(in.URL)
		if err != nil {
			return err
		}
		in.URL = base
		presented, trusted, err := api.ProbeCertificate(ctx, base)
		if err != nil {
			return fmt.Errorf("cannot reach %s: %w", base, err)
		}
		switch {
		case trusted && in.Fingerprint == "":
		case in.Fingerprint != "" && api.NormalizeFingerprint(in.Fingerprint) != presented && !trusted:
			return fmt.Errorf("the NAS presents the certificate %s, not the one expected: pairing refused", presented)
		case in.Fingerprint == "" && !*trust:
			return fmt.Errorf("the NAS certificate is not signed by a public authority: check its fingerprint %s in the web interface, then pass --fingerprint (or --trust)", presented)
		case in.Fingerprint == "":
			in.Fingerprint = presented
			fmt.Println("certificate accepted:", presented)
		}
		if trusted {
			in.Fingerprint = ""
		}
		if err := a.Pair(ctx, in); err != nil {
			return err
		}
		c := a.Config()
		fmt.Printf("paired with %s (%s) as %s\n", c.NASName, c.Server, c.User)
		return nil

	case "unpair":
		if err := a.SignOut(ctx); err != nil {
			return err
		}
		fmt.Println("signed out")
		return nil

	case "add":
		local := fs.String("local", "", "local folder")
		remote := fs.String("remote", "", "NAS folder")
		exclude := fs.String("exclude", "", "subfolders left out, comma-separated")
		encrypt := fs.Bool("encrypt", false, "make the (empty) NAS folder end-to-end encrypted")
		passFile := fs.String("passphrase-file", "", "file holding the passphrase of the encrypted folder")
		recFile := fs.String("recovery-key-file", "", "file holding the recovery key of the encrypted folder")
		_ = fs.Parse(args)
		opt := app.PairOptions{Encrypt: *encrypt, Passphrase: os.Getenv("STRATAOS_SYNC_PASSPHRASE")}
		if *passFile != "" {
			b, err := os.ReadFile(*passFile)
			if err != nil {
				return err
			}
			opt.Passphrase = strings.TrimRight(string(b), "\r\n")
		}
		if *recFile != "" {
			b, err := os.ReadFile(*recFile)
			if err != nil {
				return err
			}
			opt.RecoveryKey = string(b)
		}
		if err := a.Start(ctx); err != nil {
			return err
		}
		defer a.Stop()
		p, err := a.AddPair(*local, *remote, opt)
		if err != nil {
			return err
		}
		if p.Encrypted && *encrypt {
			k, _ := a.RecoveryKey(p.ID)
			fmt.Fprintln(os.Stderr, "Recovery key (keep it safe, it opens the folder without the passphrase):")
			fmt.Fprintln(os.Stderr, k)
		}
		if *exclude != "" {
			if err := a.UpdatePair(p.ID, nil, strings.Split(*exclude, ",")); err != nil {
				return err
			}
		}
		fmt.Println(p.ID)
		return nil

	case "recovery-key":
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("recovery-key ID (see list)")
		}
		k, err := a.RecoveryKey(fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Println(k)
		return nil

	case "passphrase":
		passFile := fs.String("passphrase-file", "", "file holding the new passphrase")
		_ = fs.Parse(args)
		if fs.NArg() != 1 || *passFile == "" {
			return errors.New("passphrase --passphrase-file FILE ID")
		}
		b, err := os.ReadFile(*passFile)
		if err != nil {
			return err
		}
		if err := a.Start(ctx); err != nil {
			return err
		}
		defer a.Stop()
		if err := a.ChangePassphrase(ctx, fs.Arg(0), strings.TrimRight(string(b), "\r\n")); err != nil {
			return err
		}
		fmt.Println("passphrase changed")
		return nil

	case "remove":
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("remove ID (see list)")
		}
		return a.RemovePair(fs.Arg(0))

	case "list":
		for _, p := range a.Config().Pairs {
			paused := ""
			if p.Paused {
				paused = " (paused)"
			}
			if p.Encrypted {
				paused += " (end-to-end encrypted)"
			}
			fmt.Printf("%s  %s  <->  %s%s\n", p.ID, p.Local, p.Remote, paused)
		}
		return nil

	case "status", "sync-once":
		confirm := fs.Bool("confirm-deletes", false, "apply deletions held by the safety brake")
		restore := fs.Bool("restore-deletes", false, "restore the files the safety brake held instead of deleting them")
		_ = fs.Parse(args)
		if !a.Paired() {
			return errors.New("not paired: run pair first")
		}
		if err := a.Start(ctx); err != nil {
			return err
		}
		defer a.Stop()
		eng := a.Engine()
		eng.Pause(false)
		if cmd == "status" {
			for _, p := range eng.Status().Pairs {
				fmt.Printf("%s  %s <-> %s\n", p.ID, p.Local, p.Remote)
			}
			return nil
		}
		err := eng.SyncOnce(ctx)
		braked := false
		for _, p := range eng.Status().Pairs {
			if p.Brake != nil {
				braked = true
				if *confirm || *restore {
					eng.ResolveBrake(p.ID, *confirm)
				}
			}
		}
		if braked && (*confirm || *restore) {
			err = eng.SyncOnce(ctx)
			braked = false
			for _, p := range eng.Status().Pairs {
				braked = braked || p.Brake != nil
			}
		}
		for _, p := range eng.Status().Pairs {
			fmt.Printf("%s  %s: %s, %d files", p.ID, p.Local, p.State, p.Files)
			if p.Conflicts > 0 {
				fmt.Printf(", %d conflict(s)", p.Conflicts)
			}
			if p.Brake != nil {
				fmt.Printf(", %d deletions held (run again with --confirm-deletes or --restore-deletes)", p.Brake.LocalDeletes+p.Brake.RemoteDeletes)
			}
			for _, s := range p.Skipped {
				fmt.Printf("\n    skipped %s: %s", s.Path, s.Reason)
			}
			fmt.Println()
		}
		if err != nil {
			return err
		}
		if braked {
			return exitCode(3)
		}
		return nil

	case "daemon":
		_ = fs.Parse(args)
		if !a.Paired() {
			return errors.New("not paired: run pair first")
		}
		if err := a.Start(ctx); err != nil {
			return err
		}
		log.Printf("synchronizing %d folder(s); Ctrl+C stops", len(a.Config().Pairs))
		<-ctx.Done()
		a.Stop()
		return nil
	}
	usage()
	return exitCode(2)
}

// setupLog writes the log to a file, started again above 5 MB.
func setupLog(file string) {
	if st, err := os.Stat(file); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(file, file+".old")
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
}
