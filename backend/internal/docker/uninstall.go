package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"strataos/internal/sys"
)

// StackMounts returns the host folders bound into the containers of a stack
// and its images, read from "compose config" (variables resolved). Without
// a usable compose, the folder variables of its catalog app are used.
func StackMounts(ctx context.Context, name string) (binds, images []string, err error) {
	if err := ValidStack(name); err != nil {
		return nil, nil, err
	}
	out, cerr := compose(ctx, name, nil, "config", "--format", "json")
	if cerr == nil {
		var cfg struct {
			Services map[string]struct {
				Image   string `json:"image"`
				Volumes []struct {
					Type   string `json:"type"`
					Source string `json:"source"`
				} `json:"volumes"`
			} `json:"services"`
		}
		if json.Unmarshal([]byte(out), &cfg) == nil {
			for _, svc := range cfg.Services {
				if svc.Image != "" && !slices.Contains(images, svc.Image) {
					images = append(images, svc.Image)
				}
				for _, v := range svc.Volumes {
					if v.Type == "bind" && filepath.IsAbs(v.Source) && !slices.Contains(binds, v.Source) {
						binds = append(binds, v.Source)
					}
				}
			}
			sort.Strings(binds)
			sort.Strings(images)
			return binds, images, nil
		}
	}
	b, _ := os.ReadFile(filepath.Join(StacksDir, name, ".strata-app"))
	if app := FindApp(strings.TrimSpace(string(b))); app != nil {
		for _, v := range app.Vars {
			if v.Type == "path" {
				if p := EnvValue(name, v.Key); filepath.IsAbs(p) {
					binds = append(binds, p)
				}
			}
		}
		sort.Strings(binds)
		return binds, nil, nil
	}
	if cerr == nil {
		cerr = fmt.Errorf("unreadable compose configuration")
	}
	return nil, nil, cerr
}

// StackVolumes lists the named Docker volumes of a stack.
func StackVolumes(ctx context.Context, name string) []string {
	if ValidStack(name) != nil || !Available() {
		return nil
	}
	out, err := sys.RunCtx(ctx, "docker", "volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+name)
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// RemoveImages deletes images no container uses any more (one in use is
// kept: docker refuses without --force).
func RemoveImages(ctx context.Context, images []string, out io.Writer) {
	for _, img := range images {
		if strings.HasPrefix(img, "-") {
			continue
		}
		if _, err := sys.RunCtx(ctx, "docker", "image", "rm", img); err != nil {
			fmt.Fprintf(out, "Image %s kept: %v\n", img, err)
		} else {
			fmt.Fprintf(out, "Image %s removed\n", img)
		}
	}
}
