package nvr

import (
	"context"
	"os"
	"strconv"
	"sync"

	"strataos/internal/mediaplay"
)

// Browser playback of recordings goes through the shared player
// (internal/mediaplay): H.264 MP4 segments are served as they are, H.265,
// MJPEG, G.711 sound or Matroska are remuxed or transcoded to fragmented
// MP4. Only the probes are kept here: a closed segment never changes, so it
// is probed once.

// probeSegment is mediaplay.Probe (a variable for the tests).
var probeSegment = mediaplay.Probe

var probeCache = struct {
	sync.Mutex
	m map[string]*mediaplay.Info
}{m: map[string]*mediaplay.Info{}}

// ProbeSegment describes the tracks of a recording, from the cache when it
// is known (keyed by path, size and modification time).
func ProbeSegment(ctx context.Context, f *os.File, info os.FileInfo) (*mediaplay.Info, error) {
	key := f.Name() + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
	probeCache.Lock()
	p, ok := probeCache.m[key]
	probeCache.Unlock()
	if ok {
		return p, nil
	}
	p, err := probeSegment(ctx, f)
	if err != nil {
		return nil, err
	}
	probeCache.Lock()
	if len(probeCache.m) > 20000 {
		probeCache.m = map[string]*mediaplay.Info{}
	}
	probeCache.m[key] = p
	probeCache.Unlock()
	return p, nil
}
