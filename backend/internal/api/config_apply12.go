package api

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"strataos/internal/backup"
	"strataos/internal/confbackup"
	"strataos/internal/firewall"
	"strataos/internal/i18n"
	"strataos/internal/photoml"
	"strataos/internal/scheduler"
	"strataos/internal/store"
)

// Import of the sections added in 0.12 (confbackup/format.go): backup
// repositories, firewall, sign-in providers, two-factor policy, S3,
// smart photos, document search and desktop preferences.

// datastores opens (or creates) the repositories of the file. A repository
// keeps its ID when it is free here, so the tasks of the file still find it;
// otherwise dsMap gives the new one.
func (im *importer) datastores() {
	im.each("datastores", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Datastores, func(d store.Datastore) bool { return d.ID == it.Key })
		d := im.p.full.Datastores[i]
		if it.Action == confbackup.Update {
			im.s.unmaskDatastore(&d, it.Target) // secrets not in the file: keep the current ones
		}
		if d.Password == masked || d.SecretKey == masked || d.VaultToken == masked {
			return fmt.Errorf("its password or keys are not in the file (export with secrets)")
		}
		if err := backup.ValidateDatastore(&d); err != nil {
			return err
		}
		for _, c := range []string{d.Verify.Cron, d.PruneCron} {
			if c != "" {
				if _, err := scheduler.ParseCron(c); err != nil {
					return err
				}
			}
		}
		ctx, cancel := context.WithTimeout(im.ctx, 2*time.Minute)
		err := backup.TestDatastore(ctx, &d)
		cancel()
		if err != nil {
			return err
		}
		var id string
		err = im.s.store.Update(func(st *store.State) error {
			if it.Action == confbackup.Update {
				x := st.Datastore(it.Target)
				if x == nil {
					return notFound("backup repository not found")
				}
				d.ID, d.Created, d.Verifies, d.Stats, d.VerifyNext, d.LastPrune = x.ID, x.Created, x.Verifies, x.Stats, x.VerifyNext, x.LastPrune
				*x = d
				id = x.ID
				return nil
			}
			id = newID(d.ID, func(id string) bool { return st.Datastore(id) != nil })
			d.ID, d.Created = id, time.Now()
			st.Datastores = append(st.Datastores, &d)
			return nil
		})
		if err == nil {
			if im.dsMap == nil {
				im.dsMap = map[string]string{}
			}
			im.dsMap[it.Key] = id
			go im.s.refreshDatastoreStats(context.Background(), id, func(string, ...any) {})
		}
		return err
	})
}

// mergeFirewall puts the chosen address sets, security groups and guests of
// the file into the current configuration, by name. The NAS firewall (host)
// is not touched here: the browser applies it on trial after the import.
func mergeFirewall(cur, file firewall.Config, keys []string) firewall.Config {
	out := cloneFW(&cur)
	for _, s := range file.IPSets {
		if slices.Contains(keys, "ipset:"+s.Name) {
			out.IPSets = append(slices.DeleteFunc(out.IPSets, func(x firewall.IPSet) bool { return x.Name == s.Name }), s)
		}
	}
	for _, g := range file.Groups {
		if slices.Contains(keys, "group:"+g.Name) {
			out.Groups = append(slices.DeleteFunc(out.Groups, func(x firewall.Group) bool { return x.Name == g.Name }), g)
		}
	}
	for _, g := range file.Guests {
		if slices.Contains(keys, "guest:"+g.Key()) {
			out.Guests = append(slices.DeleteFunc(out.Guests, func(x firewall.Guest) bool { return x.Key() == g.Key() }), g)
		}
	}
	return out
}

func (im *importer) firewall() {
	var items []confbackup.Item
	var keys []string
	for _, it := range im.findSection("firewall") {
		if !applicable(it) {
			continue
		}
		if it.Key == "host" {
			im.j.Logf("firewall: the NAS firewall is applied on trial by this browser once the import has finished")
			continue
		}
		items, keys = append(items, it), append(keys, it.Key)
	}
	if len(items) == 0 || im.ctx.Err() != nil {
		return
	}
	fwState.Lock()
	onTrial := fwState.trial != nil
	fwState.Unlock()
	var err error
	if onTrial {
		err = fmt.Errorf("a firewall change is on trial: confirm it or roll it back first")
	} else {
		next := mergeFirewall(im.s.fwConfig(), *im.p.full.Firewall, keys)
		if err = next.Validate(); err == nil {
			err = im.s.fwSave(im.ctx, next)
		}
	}
	for _, it := range items {
		if err != nil {
			im.fail("firewall", it, err)
		} else {
			im.ok("firewall", it)
		}
	}
}

func (im *importer) oidc() {
	im.each("oidc", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.OIDC, func(p store.OIDCProvider) bool { return p.ID == it.Key })
		p := im.p.full.OIDC[i]
		lost := p.ClientSecret == masked
		if lost {
			p.ClientSecret = ""
		}
		if err := validProvider(&p); err != nil {
			return err
		}
		err := im.s.store.Update(func(st *store.State) error {
			j := slices.IndexFunc(st.OIDC, func(x *store.OIDCProvider) bool { return x.ID == p.ID })
			if j >= 0 {
				old := st.OIDC[j]
				if lost {
					p.ClientSecret = old.ClientSecret
				}
				if old.Issuer != p.Issuer {
					// Links were made with the old issuer's subjects.
					for _, u := range st.Users {
						u.OIDC = slices.DeleteFunc(u.OIDC, func(l store.OIDCLink) bool { return l.Provider == p.ID })
					}
				}
				p.CreatedAt = old.CreatedAt
				st.OIDC[j] = &p
				return nil
			}
			if len(st.OIDC) >= 20 {
				return badRequest("20 providers at most")
			}
			if lost {
				p.Enabled = false // a confidential client cannot sign anyone in without its secret
			}
			p.CreatedAt = time.Now()
			st.OIDC = append(st.OIDC, &p)
			return nil
		})
		if err == nil {
			oidcClient.Forget(p.Issuer)
		}
		return err
	})
}

var (
	s3AccessRe = regexp.MustCompile(`^[A-Z0-9]{8,64}$`)
	s3SecretRe = regexp.MustCompile(`^[A-Za-z0-9_-]{20,128}$`)
)

func (im *importer) s3() {
	f := im.p.full.S3
	keys := false
	im.each("s3", func(it confbackup.Item) error {
		if it.Key == "settings" {
			// Buckets of shared folders that exist here; the service is only
			// switched on where its component is installed.
			req := map[string]any{"enabled": f.Enabled, "port": f.Port, "domain": f.Domain}
			var buckets []store.S3Bucket
			im.s.store.Read(func(st *store.State) {
				var have []store.S3Bucket
				for _, b := range f.Buckets {
					if st.Share(b.Share) != nil {
						have = append(have, b)
					} else {
						im.j.Logf("s3: bucket %s left out (no shared folder %s here)", b.Name, b.Share)
					}
				}
				buckets = mergeS3Buckets(st.S3.Buckets, have, im.j.Logf)
			})
			req["buckets"] = buckets
			if f.Enabled && !s3Installed() {
				req["enabled"] = false
				im.j.Logf("s3: the S3 service is not installed here: settings imported, service left off")
			}
			_, err := im.rp.call("PUT", "/api/s3", req)
			return err
		}
		access := strings.TrimPrefix(it.Key, "key:")
		i := slices.IndexFunc(f.Keys, func(k confbackup.S3Key) bool { return k.Access == access })
		k := f.Keys[i]
		if !s3AccessRe.MatchString(k.Access) || !s3SecretRe.MatchString(k.Secret) {
			return fmt.Errorf("invalid access key")
		}
		err := im.s.store.Update(func(st *store.State) error {
			u := st.User(k.User)
			if u == nil {
				return notFound("user not found")
			}
			for _, x := range st.Users {
				if slices.ContainsFunc(x.S3Keys, func(y store.S3Key) bool { return y.Access == k.Access }) {
					return fmt.Errorf("the access key already exists")
				}
			}
			if len(u.S3Keys) >= maxS3Keys {
				return fmt.Errorf("%d keys at most", maxS3Keys)
			}
			u.S3Keys = append(u.S3Keys, store.S3Key{Access: k.Access, Secret: k.Secret, Name: oidcText(k.Name, 64), Created: k.Created})
			return nil
		})
		keys = keys || err == nil
		return err
	})
	if keys {
		go im.s.applyS3Logged()
	}
}

// mergeS3Buckets puts the buckets of the file over the current ones, by
// shared folder. Buckets that exist only here are kept (an import removes
// nothing), and object lock, which S3 never turns off, stays on where it is
// on here (with its bucket name).
func mergeS3Buckets(cur, file []store.S3Bucket, logf func(string, ...any)) []store.S3Bucket {
	out := []store.S3Bucket{}
	names := map[string]bool{}
	for _, b := range file {
		if i := slices.IndexFunc(cur, func(c store.S3Bucket) bool { return c.Share == b.Share }); i >= 0 && cur[i].Lock {
			c := cur[i]
			if !b.Lock || b.Name != c.Name {
				logf("s3: bucket %s keeps its object lock and its name", c.Name)
			}
			if !b.Lock {
				b.LockMode, b.LockDays = c.LockMode, c.LockDays
			}
			b.Name, b.Lock, b.Versioning = c.Name, true, "enabled"
		}
		out = append(out, b)
		names[b.Name] = true
	}
	for _, c := range cur {
		if slices.ContainsFunc(file, func(b store.S3Bucket) bool { return b.Share == c.Share }) {
			continue
		}
		if names[c.Name] && c.Lock {
			// A locked bucket keeps its name: the file's bucket gives way.
			logf("s3: bucket %s of the file left out (a bucket with object lock has this name here)", c.Name)
			out = slices.DeleteFunc(out, func(b store.S3Bucket) bool { return b.Name == c.Name })
		} else if names[c.Name] {
			logf("s3: bucket %s of this NAS left out (the file uses its name)", c.Name)
			continue
		}
		out = append(out, c)
		names[c.Name] = true
	}
	return out
}

func (im *importer) photoSmart() {
	p := *im.p.full.PhotoSmart
	im.each("photoSmart", func(it confbackup.Item) error {
		if p.Enabled && !photoml.Installed() {
			p.Enabled = false
			im.j.Logf("photoSmart: the smart photos component is not installed here: settings imported, analysis left off")
		}
		_, err := im.rp.call("PUT", "/api/photos/smart", p)
		return err
	})
}

func (im *importer) desktop() {
	im.each("desktop", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Desktop, func(p confbackup.DesktopPrefs) bool { return p.User == it.Key })
		p := im.p.full.Desktop[i]
		if err := validDesktopIcons(p.Icons); err != nil {
			return err
		}
		if p.Widgets != nil {
			if err := validDesktopWidgets(*p.Widgets); err != nil {
				return err
			}
		}
		if p.Wallpaper == "custom" {
			p.Wallpaper = ""
		}
		if err := validAppearance(p.Wallpaper, p.Accent, false); err != nil {
			return err
		}
		if p.Lang != "" && !i18n.Valid(p.Lang) {
			return fmt.Errorf("invalid language")
		}
		return im.s.store.Update(func(st *store.State) error {
			u := st.User(p.User)
			if u == nil {
				return notFound("user not found")
			}
			if len(p.Icons) > 0 {
				u.Desktop = p.Icons
			}
			if p.Widgets != nil {
				u.DesktopWidgets = p.Widgets
			}
			// An uploaded wallpaper is a file of this NAS: kept.
			if p.Wallpaper != "" && u.Wallpaper != "custom" {
				u.Wallpaper = p.Wallpaper
			}
			if p.Accent != "" {
				u.Accent = p.Accent
			}
			if p.Lang != "" {
				u.Lang = p.Lang
			}
			return nil
		})
	})
}
