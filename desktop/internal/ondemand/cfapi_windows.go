package ondemand

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/windows"

	"strataos-sync/internal/engine"
)

// Windows: the Cloud Files API (cldapi.dll), the platform OneDrive uses.
// The sync root is a folder of the user ("StrataOS - NAS"); every item of
// the NAS is a placeholder there (a file with its name, size and date but
// no data), filled by FETCH_DATA when a program reads it. Explorer shows
// the cloud / on this device / always on this device icons, and its
// "Always keep on this device" and "Free up space" entries set the pin
// state, which this provider follows.
//
// Written in Go, in the agent, rather than in the Rust shell: the callbacks
// need the NAS client, the trees of the shared folders and the upload
// logic, all here and tested on Linux; the API is plain C (no COM), so
// syscalls are enough, and the pure parts (cfapi_logic.go, the structure
// layouts in cfapi_types.go) are tested on every system. Only the Explorer
// registration (StorageProviderSyncRootManager, a WinRT API) is done by
// the shell (src-tauri/src/syncroot.rs), where the windows crate gives
// checked bindings; without the shell the agent registers a plain sync
// root itself (CfRegisterSyncRoot: no navigation pane entry).
//
// Population is "always full": the agent creates every placeholder itself
// from the tree it already keeps (the change feed), so folders open at
// once, offline too, and Windows never asks for listings. Hydration is
// "full": a file is either in the cloud or on this device.

var (
	cldapi                   = windows.NewLazySystemDLL("cldapi.dll")
	procCfRegisterSyncRoot   = cldapi.NewProc("CfRegisterSyncRoot")
	procCfUnregisterSyncRoot = cldapi.NewProc("CfUnregisterSyncRoot")
	procCfConnectSyncRoot    = cldapi.NewProc("CfConnectSyncRoot")
	procCfDisconnectSyncRoot = cldapi.NewProc("CfDisconnectSyncRoot")
	procCfGetSyncRootInfo    = cldapi.NewProc("CfGetSyncRootInfoByPath")
	procCfExecute            = cldapi.NewProc("CfExecute")
	procCfCreatePlaceholders = cldapi.NewProc("CfCreatePlaceholders")
	procCfUpdatePlaceholder  = cldapi.NewProc("CfUpdatePlaceholder")
	procCfConvertToPh        = cldapi.NewProc("CfConvertToPlaceholder")
	procCfSetPinState        = cldapi.NewProc("CfSetPinState")
	procCfHydratePlaceholder = cldapi.NewProc("CfHydratePlaceholder")
	procCfDehydratePh        = cldapi.NewProc("CfDehydratePlaceholder")
	procCfGetPlaceholderInfo = cldapi.NewProc("CfGetPlaceholderInfo")
	procCfStateFromFindData  = cldapi.NewProc("CfGetPlaceholderStateFromFindData")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procFindFirstFileW       = kernel32.NewProc("FindFirstFileW")
	procFindNextFileW        = kernel32.NewProc("FindNextFileW")
	// Provider ID of the plain registration (any fixed GUID).
	cfProviderID        = [16]byte{0x5b, 0x1e, 0x36, 0x8c, 0x2f, 0x4a, 0x4e, 0x1d, 0x9a, 0x3c, 0x51, 0x7e, 0x0b, 0x6d, 0x22, 0x91}
	transferPiece int64 = 4 << 20
)

func supported() (bool, string) {
	if cldapi.Load() != nil {
		return false, "" // before Windows 10 1709
	}
	return true, "cfapi"
}

// hr turns an HRESULT into an error.
func hr(r uintptr) error {
	v := uint32(r)
	if int32(v) >= 0 {
		return nil
	}
	if v>>16&0x1fff == 7 { // FACILITY_WIN32
		return windows.Errno(v & 0xffff)
	}
	return fmt.Errorf("HRESULT %#x", v)
}

// Callbacks: one Go callback per kind for every connection (NewCallback
// cannot be freed), dispatched by the context given to CfConnectSyncRoot.
var (
	cbOnce      sync.Once
	cbTable     []cfCallbackRegistration
	cbMu        sync.Mutex
	cbProviders = map[uintptr]*cfProvider{}
	cbNext      uintptr
)

func callbackTable() []cfCallbackRegistration {
	cbOnce.Do(func() {
		cb := func(kind uint32) uintptr {
			return windows.NewCallback(func(info *cfCallbackInfo, params *cfCallbackParams) uintptr {
				cbMu.Lock()
				p := cbProviders[info.CallbackContext]
				cbMu.Unlock()
				if p != nil {
					p.callback(kind, info, params)
				}
				return 0
			})
		}
		cbTable = []cfCallbackRegistration{
			{Type: cfCallbackFetchData, Callback: cb(cfCallbackFetchData)},
			{Type: cfCallbackCancelFetchData, Callback: cb(cfCallbackCancelFetchData)},
			{Type: cfCallbackNotifyDelete, Callback: cb(cfCallbackNotifyDelete)},
			{Type: cfCallbackNotifyRename, Callback: cb(cfCallbackNotifyRename)},
			{Type: cfCallbackNone},
		}
	})
	return cbTable
}

type cfProvider struct {
	s     *Service
	o     ProviderOptions
	root  string
	id    string
	ctxID uintptr

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	regCh  chan struct{}

	mu        sync.Mutex
	state     string
	errMsg    string
	key       int64
	connected bool
	transfers map[int64]context.CancelFunc
	pending   map[string]time.Time // local paths changed, by last event
	remote    map[string]bool      // folders to compare with the NAS ("Share/…"), deep or not
	warned    map[string]bool

	rec     sync.Mutex // one reconciliation at a time
	watcher *fsnotify.Watcher
	watched map[string]bool
}

// StartProvider connects the sync root of the account (registering a plain
// one when the shell does not) and keeps its placeholders like the NAS.
func StartProvider(s *Service, o ProviderOptions) (Provider, error) {
	if ok, _ := supported(); !ok {
		return nil, ErrUnsupported
	}
	if o.Root == "" {
		return nil, errors.New("no folder for files on demand")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("user SID: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &cfProvider{s: s, o: o, root: root, id: SyncRootID(u.User.Sid.String(), o.Account), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), regCh: make(chan struct{}, 1), state: "starting", transfers: map[int64]context.CancelFunc{},
		pending: map[string]time.Time{}, remote: map[string]bool{}, warned: map[string]bool{}, watched: map[string]bool{}}
	cbMu.Lock()
	cbNext++
	p.ctxID = cbNext
	cbProviders[p.ctxID] = p
	cbMu.Unlock()
	s.OnChange = p.onRemote
	go p.run()
	return p, nil
}

func (p *cfProvider) Info() ProviderInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProviderInfo{Kind: "cfapi", Root: p.root, State: p.state, Error: p.errMsg, SyncRootID: p.id}
}

func (p *cfProvider) setState(st string, err error) {
	p.mu.Lock()
	p.state, p.errMsg = st, ""
	if err != nil {
		p.errMsg = err.Error()
	}
	p.mu.Unlock()
}

// Registered: the shell registered the sync root; connect now.
func (p *cfProvider) Registered() error {
	select {
	case p.regCh <- struct{}{}:
	default:
	}
	return nil
}

func (p *cfProvider) Close() error {
	p.s.OnChange = nil
	p.cancel()
	<-p.done
	cbMu.Lock()
	delete(cbProviders, p.ctxID)
	cbMu.Unlock()
	return nil
}

func (p *cfProvider) registered() bool {
	path, _ := windows.UTF16PtrFromString(p.root)
	var info [8]byte // CF_SYNC_ROOT_BASIC_INFO
	var n uint32
	r, _, _ := procCfGetSyncRootInfo.Call(uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&info[0])), 8, uintptr(unsafe.Pointer(&n)))
	return hr(r) == nil
}

// register makes a plain sync root (no Explorer navigation entry).
func (p *cfProvider) register() error {
	path, _ := windows.UTF16PtrFromString(p.root)
	name, _ := windows.UTF16PtrFromString(p.o.Name)
	ver, _ := windows.UTF16PtrFromString("1")
	ident := []byte("strataos:" + p.o.Account)
	reg := cfSyncRegistration{ProviderName: uintptr(unsafe.Pointer(name)), ProviderVersion: uintptr(unsafe.Pointer(ver)),
		SyncRootIdentity: uintptr(unsafe.Pointer(&ident[0])), SyncRootIdentityLength: uint32(len(ident)), ProviderID: cfProviderID}
	reg.StructSize = uint32(unsafe.Sizeof(reg))
	pol := cfSyncPolicies{HydrationPrimary: cfHydrationFull, PopulationPrimary: cfPopulationAlwaysFull}
	pol.StructSize = uint32(unsafe.Sizeof(pol))
	r, _, _ := procCfRegisterSyncRoot.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&reg)), uintptr(unsafe.Pointer(&pol)), cfRegisterMarkInSyncRoot)
	runtime.KeepAlive(name)
	runtime.KeepAlive(ver)
	runtime.KeepAlive(ident)
	return hr(r)
}

func (p *cfProvider) connect() error {
	path, _ := windows.UTF16PtrFromString(p.root)
	t := callbackTable()
	var key int64
	r, _, _ := procCfConnectSyncRoot.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&t[0])), p.ctxID,
		cfConnectRequireProcessInfo|cfConnectRequireFullFilePath, uintptr(unsafe.Pointer(&key)))
	if err := hr(r); err != nil {
		return err
	}
	p.mu.Lock()
	p.key, p.connected = key, true
	p.mu.Unlock()
	return nil
}

func (p *cfProvider) run() {
	defer close(p.done)
	defer func() {
		p.mu.Lock()
		key, ok := p.key, p.connected
		p.connected = false
		for _, c := range p.transfers {
			c()
		}
		p.mu.Unlock()
		if ok {
			procCfDisconnectSyncRoot.Call(uintptr(key))
		}
	}()
	// Registration: the shell's (Explorer integration), else ours.
	if !p.registered() {
		if p.o.Shell {
			p.setState("register", nil)
			select {
			case <-p.ctx.Done():
				return
			case <-p.regCh:
			case <-time.After(20 * time.Second):
			}
		}
		if !p.registered() {
			if err := p.register(); err != nil {
				p.setState("error", fmt.Errorf("register the sync root: %w", err))
				log.Printf("files on demand: register %s: %v", p.root, err)
				return
			}
		}
	}
	if err := p.connect(); err != nil {
		p.setState("error", fmt.Errorf("connect the sync root: %w", err))
		log.Printf("files on demand: connect %s: %v", p.root, err)
		return
	}
	var events <-chan fsnotify.Event
	if w, err := fsnotify.NewWatcher(); err == nil {
		p.watcher = w
		events = w.Events
		defer w.Close()
	}
	p.setState("ready", nil)
	p.reconcile("", true)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	full := time.NewTicker(5 * time.Minute)
	defer full.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case ev := <-events:
			p.mu.Lock()
			p.pending[ev.Name] = time.Now()
			p.mu.Unlock()
		case <-full.C:
			p.reconcile("", true) // also catches what the watcher missed
		case <-tick.C:
			p.mu.Lock()
			dirs := p.remote
			p.remote = map[string]bool{}
			var local []string
			for path, t := range p.pending {
				if time.Since(t) > 3*time.Second { // a file being written settles first
					local = append(local, path)
					delete(p.pending, path)
				}
			}
			p.mu.Unlock()
			for d, deep := range dirs {
				p.reconcile(d, deep)
			}
			for _, path := range local {
				p.local(path)
			}
		}
	}
}

// onRemote queues the folders to compare after changes on the NAS.
func (p *cfProvider) onRemote(ch []Change) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range ch {
		par := parentOf(c.Path)
		if !p.remote[par] {
			p.remote[par] = false
		}
		if c.Dir && c.Kind == Added {
			p.remote[c.Path] = true
		}
	}
}

func (p *cfProvider) abs(rel string) string {
	if rel == "" {
		return p.root
	}
	return filepath.Join(p.root, filepath.FromSlash(rel))
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// ---- Folders: placeholders like the NAS ----

// listLocal reads a folder of the sync root with the placeholder states.
func (p *cfProvider) listLocal(dir string) ([]localEntry, error) {
	pat, _ := windows.UTF16PtrFromString(filepath.Join(p.abs(dir), "*"))
	var fd win32FindData
	h, _, e := procFindFirstFileW.Call(uintptr(unsafe.Pointer(pat)), uintptr(unsafe.Pointer(&fd)))
	if windows.Handle(h) == windows.InvalidHandle {
		if errors.Is(e, windows.ERROR_FILE_NOT_FOUND) {
			return nil, nil
		}
		return nil, e
	}
	defer windows.FindClose(windows.Handle(h))
	var out []localEntry
	for {
		name := windows.UTF16ToString(fd.FileName[:])
		if name != "." && name != ".." {
			st, _, _ := procCfStateFromFindData.Call(uintptr(unsafe.Pointer(&fd)))
			l := localEntry{Name: name, Dir: fd.FileAttributes&attrDirectory != 0,
				Size: int64(fd.FileSizeHigh)<<32 | int64(fd.FileSizeLow)}
			if uint32(st) != cfStateInvalid {
				l.Placeholder = uint32(st)&cfStatePlaceholder != 0
				l.InSync = uint32(st)&cfStateInSync != 0
			}
			if l.Placeholder && !l.Dir {
				if _, v, ok := p.identity(joinRel(dir, name)); ok {
					l.Version = v
				}
			}
			out = append(out, l)
		}
		r, _, _ := procFindNextFileW.Call(h, uintptr(unsafe.Pointer(&fd)))
		if r == 0 {
			break
		}
	}
	return out, nil
}

// open opens an item of the sync root without reading its data (which
// would hydrate it).
func (p *cfProvider) open(rel string, write bool) (windows.Handle, error) {
	path, err := windows.UTF16PtrFromString(p.abs(rel))
	if err != nil {
		return 0, err
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES)
	if write {
		access |= windows.FILE_WRITE_ATTRIBUTES | windows.WRITE_DAC
	}
	return windows.CreateFile(path, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

// identity reads the identity of a placeholder (CF_PLACEHOLDER_BASIC_INFO:
// PinState, InSyncState, FileId, SyncRootFileId, FileIdentityLength at 24,
// FileIdentity at 28).
func (p *cfProvider) identity(rel string) (dir bool, version string, ok bool) {
	h, err := p.open(rel, false)
	if err != nil {
		return false, "", false
	}
	defer windows.CloseHandle(h)
	buf := make([]byte, 4200)
	var n uint32
	r, _, _ := procCfGetPlaceholderInfo.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))
	if hr(r) != nil || n < 28 {
		return false, "", false
	}
	l := *(*uint32)(unsafe.Pointer(&buf[24]))
	if 28+int(l) > len(buf) {
		return false, "", false
	}
	return decodeIdentity(buf[28 : 28+l])
}

func metadata(it Item) cfFSMetadata {
	t := fileTime(it.MTime)
	m := cfFSMetadata{CreationTime: t, LastAccessTime: t, LastWriteTime: t, ChangeTime: t, FileAttributes: attrNormal, FileSize: it.Size}
	if it.Dir {
		m.FileAttributes, m.FileSize = attrDirectory, 0
	} else if !it.Writable {
		m.FileAttributes = attrReadonly
	}
	return m
}

// create makes placeholders for NAS items in a folder.
func (p *cfProvider) create(dir string, items []*Item) error {
	if len(items) == 0 {
		return nil
	}
	infos := make([]cfPlaceholderCreateInfo, len(items))
	var names []*uint16
	var idents [][]byte
	for i, it := range items {
		name, _ := windows.UTF16PtrFromString(it.Name)
		ident := encodeIdentity(it.Dir, it.Version)
		names, idents = append(names, name), append(idents, ident)
		flags := uint32(cfPlaceholderMarkInSync)
		if it.Dir {
			flags |= cfPlaceholderDisableOnDemandPopulation
		}
		infos[i] = cfPlaceholderCreateInfo{RelativeFileName: uintptr(unsafe.Pointer(name)), FsMetadata: metadata(*it),
			FileIdentity: uintptr(unsafe.Pointer(&ident[0])), FileIdentityLength: uint32(len(ident)), Flags: flags}
	}
	base, _ := windows.UTF16PtrFromString(p.abs(dir))
	var done uint32
	r, _, _ := procCfCreatePlaceholders.Call(uintptr(unsafe.Pointer(base)), uintptr(unsafe.Pointer(&infos[0])), uintptr(len(infos)), 0, uintptr(unsafe.Pointer(&done)))
	runtime.KeepAlive(names)
	runtime.KeepAlive(idents)
	runtime.KeepAlive(base)
	for i := range infos {
		if err := hr(uintptr(uint32(infos[i].Result))); err != nil && err != windows.ERROR_ALREADY_EXISTS && err != windows.ERROR_FILE_EXISTS {
			log.Printf("files on demand: placeholder %s: %v", joinRel(dir, items[i].Name), err)
		}
	}
	return hr(r)
}

// update gives a placeholder the metadata of a NAS version (dehydrate: and
// frees its old data) and marks it in sync.
func (p *cfProvider) update(rel string, it Item, dehydrate bool) error {
	h, err := p.open(rel, true)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	m := metadata(it)
	ident := encodeIdentity(it.Dir, it.Version)
	flags := uintptr(cfUpdateMarkInSync)
	if dehydrate && !it.Dir {
		flags |= cfUpdateDehydrate
	}
	r, _, _ := procCfUpdatePlaceholder.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(unsafe.Pointer(&ident[0])), uintptr(len(ident)), 0, 0, flags, 0, 0)
	runtime.KeepAlive(ident)
	return hr(r)
}

// convert turns a file or folder of this computer into an in-sync placeholder.
func (p *cfProvider) convert(rel string, it Item) error {
	h, err := p.open(rel, true)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	ident := encodeIdentity(it.Dir, it.Version)
	r, _, _ := procCfConvertToPh.Call(uintptr(h), uintptr(unsafe.Pointer(&ident[0])), uintptr(len(ident)), cfConvertMarkInSync, 0, 0)
	runtime.KeepAlive(ident)
	return hr(r)
}

// reconcile makes a folder of the sync root match the NAS (deep: and
// the folders below it).
func (p *cfProvider) reconcile(dir string, deep bool) {
	if p.ctx.Err() != nil {
		return
	}
	p.rec.Lock()
	defer p.rec.Unlock()
	p.reconcileLocked(dir, deep)
}

func (p *cfProvider) reconcileLocked(dir string, deep bool) {
	if p.ctx.Err() != nil {
		return
	}
	p.watch(p.abs(dir))
	remote, err := p.s.List(dir)
	if err != nil {
		return // gone: its parent removes it
	}
	local, err := p.listLocal(dir)
	if err != nil {
		log.Printf("files on demand: list %s: %v", p.abs(dir), err)
		return
	}
	var create []*Item
	var recurse []string
	for _, op := range planDir(local, remote) {
		rel := joinRel(dir, op.Name)
		switch op.Kind {
		case opCreate:
			create = append(create, op.Item)
			if op.Item.Dir {
				recurse = append(recurse, rel)
			}
		case opUpdate:
			if err := p.update(rel, *op.Item, true); err != nil {
				log.Printf("files on demand: update %s: %v", rel, err)
			} else if p.pinned(rel) {
				go p.hydrate(rel)
			}
		case opDeleteHere:
			// Our own deletion: NOTIFY_DELETE sees this process and agrees.
			if err := os.RemoveAll(p.abs(rel)); err != nil {
				log.Printf("files on demand: remove %s: %v", rel, err)
			}
		case opUpload, opMkdirThere:
			if dir != "" && !ignoredLocal(op.Name) {
				p.mu.Lock()
				p.pending[p.abs(rel)] = time.Time{} // sent by the local loop
				p.mu.Unlock()
			}
		case opRecurse:
			if deep {
				recurse = append(recurse, rel)
			}
		}
	}
	if err := p.create(dir, create); err != nil {
		log.Printf("files on demand: placeholders in %s: %v", p.abs(dir), err)
	}
	if dir != "" && len(create) > 0 && p.pinned(dir) {
		for _, it := range create {
			if !it.Dir {
				go p.hydrate(joinRel(dir, it.Name))
			}
		}
	}
	for _, d := range recurse {
		p.reconcileLocked(d, true)
	}
}

func (p *cfProvider) watch(dir string) {
	if p.watcher == nil || p.watched[dir] {
		return
	}
	if p.watcher.Add(dir) == nil {
		p.watched[dir] = true
	}
}

func (p *cfProvider) attrs(rel string) uint32 {
	path, _ := windows.UTF16PtrFromString(p.abs(rel))
	a, err := windows.GetFileAttributes(path)
	if err != nil {
		return 0
	}
	return a
}

func (p *cfProvider) pinned(rel string) bool { return p.attrs(rel)&attrPinned != 0 }

// ---- Changes made on this computer ----

// local looks at a path that changed here: sends new and changed files
// and folders, follows the pin state Explorer set.
func (p *cfProvider) local(abs string) {
	rel, ok := winRel(p.root, abs)
	if !ok || rel == "" || p.ctx.Err() != nil {
		return
	}
	p.rec.Lock()
	defer p.rec.Unlock()
	fi, err := os.Lstat(abs)
	if err != nil {
		return // deleted: placeholders through NOTIFY_DELETE, the rest was never sent
	}
	if !strings.Contains(rel, "/") {
		if fi.IsDir() {
			if _, err := p.s.Stat(rel); err == nil {
				return // a shared folder
			}
		}
		if !p.warned[rel] {
			p.warned[rel] = true
			log.Printf("files on demand: %s: only shared folders at the top", rel)
		}
		return
	}
	if ignoredLocal(filepath.Base(abs)) || fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	dir := parentOf(rel)
	l, err := p.listLocal(dir)
	if err != nil {
		return
	}
	var e *localEntry
	for i := range l {
		if strings.EqualFold(l[i].Name, filepath.Base(abs)) {
			e = &l[i]
		}
	}
	if e == nil {
		return
	}
	a := p.attrs(rel)
	switch {
	case e.Dir && !e.Placeholder:
		it, err := p.s.Mkdir(p.ctx, rel)
		if err != nil && !errors.Is(err, ErrExists) {
			p.fail(rel, err)
			return
		}
		it.Dir = true
		if err := p.convert(rel, it); err != nil {
			log.Printf("files on demand: convert %s: %v", rel, err)
		}
		p.reconcileLocked(rel, true) // and send what is inside
	case e.Dir:
		if a&attrPinned != 0 {
			go p.hydrateAll(rel)
		}
	case !e.Placeholder || !e.InSync:
		p.send(rel, e)
	case a&attrPinned != 0 && a&attrRecallOnDataAccess != 0:
		go p.hydrate(rel)
	case a&attrUnpinned != 0 && a&attrRecallOnDataAccess == 0:
		p.dehydrate(rel)
	}
}

func (p *cfProvider) fail(rel string, err error) {
	log.Printf("files on demand: %s: %v", rel, err)
	if p.s.Notify != nil && !errors.Is(err, context.Canceled) {
		p.s.Notify(engine.Event{Kind: "error", Path: rel, Msg: err.Error()})
	}
}

// send uploads a file of this computer (new, or changed since its
// placeholder was in sync) and makes it an in-sync placeholder again.
func (p *cfProvider) send(rel string, e *localEntry) {
	abs := p.abs(rel)
	// Still open by the program writing it: later.
	path, _ := windows.UTF16PtrFromString(abs)
	h, err := windows.CreateFile(path, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		p.mu.Lock()
		p.pending[abs] = time.Now()
		p.mu.Unlock()
		return
	}
	windows.CloseHandle(h)
	f, err := os.Open(abs)
	if err != nil {
		return
	}
	before, err := f.Stat()
	if err != nil {
		f.Close()
		return
	}
	ifMatch := ""
	if e.Placeholder {
		ifMatch = e.Version
	}
	it, err := p.s.Upload(p.ctx, rel, f, before.Size(), before.ModTime().UnixMilli(), ifMatch)
	f.Close()
	if err != nil {
		p.fail(rel, err)
		return
	}
	after, err := os.Stat(abs)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		p.mu.Lock()
		p.pending[abs] = time.Now() // written again meanwhile: send again
		p.mu.Unlock()
		return
	}
	if it.Path != rel {
		// The NAS had another version: ours went under a conflict name;
		// this file becomes that copy, the NAS version comes back here.
		if err := os.Rename(abs, p.abs(it.Path)); err != nil {
			log.Printf("files on demand: conflict copy %s: %v", it.Path, err)
			return
		}
		rel = it.Path
	}
	if e.Placeholder {
		err = p.update(rel, it, false)
	} else {
		err = p.convert(rel, it)
	}
	if err != nil {
		log.Printf("files on demand: in sync %s: %v", rel, err)
	}
	p.reconcileLocked(parentOf(rel), false)
}

// ---- Keep on this device / free up space ----

// Pin sets the pin state (as Explorer's menu does) and downloads or frees.
func (p *cfProvider) Pin(rel string, on bool) error {
	h, err := p.open(rel, true)
	if err != nil {
		return err
	}
	st := uintptr(cfPinUnpinned)
	if on {
		st = cfPinPinned
	}
	r, _, _ := procCfSetPinState.Call(uintptr(h), st, cfPinRecurse, 0)
	windows.CloseHandle(h)
	if err := hr(r); err != nil {
		return err
	}
	if on {
		go p.hydrateAll(rel)
	} else {
		p.dehydrateAll(rel)
	}
	return nil
}

func (p *cfProvider) hydrate(rel string) {
	h, err := p.open(rel, false)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	// StartingOffset 0, Length -1: the whole file (through FETCH_DATA).
	r, _, _ := procCfHydratePlaceholder.Call(uintptr(h), 0, ^uintptr(0), 0, 0)
	if err := hr(r); err != nil {
		log.Printf("files on demand: download %s: %v", rel, err)
	}
}

func (p *cfProvider) dehydrate(rel string) {
	h, err := p.open(rel, true)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	r, _, _ := procCfDehydratePh.Call(uintptr(h), 0, ^uintptr(0), 0, 0)
	if err := hr(r); err != nil {
		log.Printf("files on demand: free %s: %v", rel, err)
	}
}

func (p *cfProvider) eachFile(rel string, f func(string)) {
	it, err := p.s.Stat(rel)
	if err != nil {
		return
	}
	if !it.Dir {
		f(rel)
		return
	}
	p.s.Walk(rel, func(c Item) {
		if !c.Dir {
			f(c.Path)
		}
	})
}

func (p *cfProvider) hydrateAll(rel string)   { p.eachFile(rel, p.hydrate) }
func (p *cfProvider) dehydrateAll(rel string) { p.eachFile(rel, p.dehydrate) }

// ---- Callbacks of the platform (on its threads: answered from goroutines) ----

// ptrAt reads a pointer the platform stored in a uintptr field.
func ptrAt(f *uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(f)) }

func wstr(f *uintptr) string {
	if *f == 0 {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(ptrAt(f)))
}

func (p *cfProvider) callback(kind uint32, info *cfCallbackInfo, params *cfCallbackParams) {
	vol := wstr(&info.VolumeDosName)
	rel, _ := winRel(p.root, vol+wstr(&info.NormalizedPath))
	op := cfOperationInfo{ConnectionKey: info.ConnectionKey, TransferKey: info.TransferKey, RequestKey: info.RequestKey}
	op.StructSize = uint32(unsafe.Sizeof(op))
	self := false
	if info.ProcessInfo != 0 { // CF_PROCESS_INFO: StructSize, ProcessId
		self = *(*uint32)(unsafe.Add(ptrAt(&info.ProcessInfo), 4)) == uint32(os.Getpid())
	}
	switch kind {
	case cfCallbackFetchData:
		var ident []byte
		if info.FileIdentity != 0 && info.FileIdentityLength > 0 {
			ident = append([]byte(nil), unsafe.Slice((*byte)(ptrAt(&info.FileIdentity)), info.FileIdentityLength)...)
		}
		_, version, _ := decodeIdentity(ident)
		go p.fetch(op, rel, version, params.A, params.B, info.FileSize)
	case cfCallbackCancelFetchData:
		p.mu.Lock()
		if c := p.transfers[info.TransferKey]; c != nil {
			c()
		}
		p.mu.Unlock()
	case cfCallbackNotifyDelete:
		op.Type = cfOpAckDelete
		if self || rel == "" {
			p.ack(op, statusSuccess)
			return
		}
		go func() { p.ack(op, p.status(p.s.Delete(p.ctx, rel))) }()
	case cfCallbackNotifyRename:
		op.Type = cfOpAckRename
		if self {
			p.ack(op, statusSuccess)
			return
		}
		if params.Flags&cfRenameFlagTargetInScope == 0 {
			// Out of the sync root: a placeholder may have no data. Copy instead.
			p.ack(op, statusAccessDenied)
			return
		}
		to, _ := winRel(p.root, vol+wstr((*uintptr)(unsafe.Pointer(&params.A))))
		go func() {
			var err error
			if _, e := p.s.Stat(to); e == nil && !strings.EqualFold(rel, to) {
				err = p.s.Replace(p.ctx, rel, to)
			} else {
				err = p.s.Move(p.ctx, rel, to)
			}
			p.ack(op, p.status(err))
		}()
	}
}

// status is the NTSTATUS answered for an operation on the NAS.
func (p *cfProvider) status(err error) int32 {
	switch {
	case err == nil, errors.Is(err, ErrNotFound):
		return statusSuccess
	case errors.Is(err, ErrReadOnly), errors.Is(err, ErrNotEmpty), errors.Is(err, ErrExists):
		return statusAccessDenied
	case errors.Is(err, ErrCrossShare):
		return statusNotSameDevice // Explorer copies then deletes
	}
	log.Printf("files on demand: %v", err)
	return statusCloudUnavailable
}

func (p *cfProvider) ack(op cfOperationInfo, status int32) {
	par := cfOperationParams{CompletionStatus: status}
	par.ParamSize = 16 // CF_SIZE_OF_OP_PARAM(AckDelete)
	r, _, _ := procCfExecute.Call(uintptr(unsafe.Pointer(&op)), uintptr(unsafe.Pointer(&par)))
	if err := hr(r); err != nil {
		log.Printf("files on demand: acknowledge: %v", err)
	}
}

// fetch answers FETCH_DATA: the bytes asked, from the NAS, in 4 KiB-aligned
// pieces; an error fails the read of the program that asked.
func (p *cfProvider) fetch(op cfOperationInfo, rel, version string, off, length, size int64) {
	op.Type = cfOpTransferData
	ctx, cancel := context.WithCancel(p.ctx)
	p.mu.Lock()
	p.transfers[op.TransferKey] = cancel
	p.mu.Unlock()
	defer func() {
		cancel()
		p.mu.Lock()
		delete(p.transfers, op.TransferKey)
		p.mu.Unlock()
	}()
	chunks := transferChunks(off, length, size, transferPiece)
	for i, c := range chunks {
		buf := &memWriter{b: make([]byte, 0, c.Len())}
		err := p.s.Fetch(ctx, rel, version, c.Off, c.Len(), buf)
		if err == nil && int64(len(buf.b)) != c.Len() {
			err = errors.New("short read")
		}
		par := cfOperationParams{Offset: c.Off, Length: c.Len()}
		par.ParamSize = 40 // CF_SIZE_OF_OP_PARAM(TransferData)
		if err != nil {
			if errors.Is(err, ErrStale) {
				p.onRemote([]Change{{Path: rel, Kind: Modified}})
			}
			if ctx.Err() == nil {
				log.Printf("files on demand: download %s: %v", rel, err)
			}
			par.CompletionStatus = statusCloudUnavailable
			par.Offset, par.Length = c.Off, chunks[len(chunks)-1].End-c.Off
			procCfExecute.Call(uintptr(unsafe.Pointer(&op)), uintptr(unsafe.Pointer(&par)))
			return
		}
		par.Buffer = uintptr(unsafe.Pointer(&buf.b[0]))
		r, _, _ := procCfExecute.Call(uintptr(unsafe.Pointer(&op)), uintptr(unsafe.Pointer(&par)))
		runtime.KeepAlive(buf)
		if err := hr(r); err != nil {
			log.Printf("files on demand: transfer %s (%d/%d): %v", rel, i+1, len(chunks), err)
			return
		}
	}
}

type memWriter struct{ b []byte }

func (m *memWriter) Write(p []byte) (int, error) {
	m.b = append(m.b, p...)
	return len(p), nil
}
