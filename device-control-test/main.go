//go:build windows

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

//go:embed icon.ico
var trayIconBytes []byte

const (
	addr      = "127.0.0.1:17836"
	origin    = "https://xdreemb52.vercel.app"
	dashboard = origin + "/device-control/"
	appName   = "التحكم في الجهاز"
	version   = "0.9.7-direct"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	winmm                   = syscall.NewLazyDLL("winmm.dll")
	shell32                 = syscall.NewLazyDLL("shell32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procKeybd               = user32.NewProc("keybd_event")
	procBeep                = user32.NewProc("MessageBeep")
	procPlay                = winmm.NewProc("PlaySoundW")
	procShell               = shell32.NewProc("ShellExecuteW")
	procMutex               = kernel32.NewProc("CreateMutexW")
	procLastErr             = kernel32.NewProc("GetLastError")
	procGetModule           = kernel32.NewProc("GetModuleHandleW")
	procRegisterClass       = user32.NewProc("RegisterClassExW")
	procCreateWindow        = user32.NewProc("CreateWindowExW")
	procDefWindow           = user32.NewProc("DefWindowProcW")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMsg        = user32.NewProc("TranslateMessage")
	procDispatchMsg         = user32.NewProc("DispatchMessageW")
	procLoadIcon            = user32.NewProc("LoadIconW")
	procLoadImage           = user32.NewProc("LoadImageW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procPostQuit            = user32.NewProc("PostQuitMessage")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForeground       = user32.NewProc("SetForegroundWindow")
	procCreateMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenu          = user32.NewProc("AppendMenuW")
	procTrackMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procIsWindowVis         = user32.NewProc("IsWindowVisible")
	procGetTextLen          = user32.NewProc("GetWindowTextLengthW")
	procGetText             = user32.NewProc("GetWindowTextW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procShellNotify         = shell32.NewProc("Shell_NotifyIconW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfo      = user32.NewProc("GetMonitorInfoW")
)

const (
	keyUp       = 0x0002
	sndAsync    = 0x0001
	sndNoDef    = 0x0002
	sndFile     = 0x00020000
	showNormal  = 1
	errExists   = 183
	maxSettings = 1 << 20

	wmDestroy       = 0x0002
	wmCommand       = 0x0111
	wmUser          = 0x0400
	wmTray          = wmUser + 52
	wmLButtonUp     = 0x0202
	wmLButtonDbl    = 0x0203
	wmRButtonUp     = 0x0205
	nimAdd          = 0x00000000
	nimDelete       = 0x00000002
	nifMessage      = 0x00000001
	nifIcon         = 0x00000002
	nifTip          = 0x00000004
	mfString        = 0x00000000
	tpmRightButton  = 0x00000002
	tpmRetCmd       = 0x00000100
	cmdOpenDash     = 1001
	cmdExitApp      = 1002
	idiApplication  = 32512
	imageIcon       = 1
	lrLoadFromFile  = 0x00000010
	lrDefaultSize   = 0x00000040
	nimSetVersion   = 0x00000004
	notifyVersion4  = 4
	swRestore       = 9
	smCxScreen      = 0
	smCyScreen      = 1
	monitorPrimary  = 1
	createNoWindow  = 0x08000000
	detachedProcess = 0x00000008
)

type LegacyAction struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Effect struct {
	Type       string   `json:"type"`
	Value      string   `json:"value,omitempty"`
	Key        string   `json:"key,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	MediaID    string   `json:"mediaId,omitempty"`
	Text       string   `json:"text,omitempty"`
	Sound      string   `json:"sound,omitempty"`
	DelayMs    int      `json:"delayMs,omitempty"`
	DurationMs int      `json:"durationMs,omitempty"`
	DisplayID  string   `json:"displayId,omitempty"`
	Position   string   `json:"position,omitempty"`
	X          int      `json:"x,omitempty"`
	Y          int      `json:"y,omitempty"`
	Width      int      `json:"width,omitempty"`
	Height     int      `json:"height,omitempty"`
	Size       int      `json:"size,omitempty"`
}

type Command struct {
	ID       string       `json:"id"`
	Name     string       `json:"name,omitempty"`
	Trigger  string       `json:"trigger"`
	Action   LegacyAction `json:"action,omitempty"`
	Effects  []Effect     `json:"effects,omitempty"`
	Sound    string       `json:"sound,omitempty"`
	Cooldown float64      `json:"cooldown"`
	Enabled  bool         `json:"enabled"`
}

type Settings struct {
	Platform string    `json:"platform"`
	Source   string    `json:"source"`
	Enabled  bool      `json:"enabled"`
	MediaDir string    `json:"mediaDir,omitempty"`
	Commands []Command `json:"commands"`
}

type Event struct {
	Seq      uint64 `json:"seq"`
	Type     string `json:"type"`
	Platform string `json:"platform,omitempty"`
	Status   string `json:"status,omitempty"`
	User     string `json:"user,omitempty"`
	Text     string `json:"text,omitempty"`
	ID       string `json:"id,omitempty"`
	Time     int64  `json:"time"`
}

type MediaItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Ext       string `json:"ext"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"createdAt"`
	Path      string `json:"-"`
}

type Display struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Primary bool   `json:"primary"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type App struct {
	mu            sync.RWMutex
	settings      Settings
	settingsPath  string
	mediaPath     string
	mediaDir      string
	media         []MediaItem
	events        []Event
	seq           uint64
	cool          map[string]time.Time
	ytCancel      context.CancelFunc
	ttCancel      context.CancelFunc
	srv           *http.Server
	lastHeartbeat int64
	heartbeatSeen uint32
}

func main() {
	singleInstance()
	a := newApp()
	go a.ensureDesktopShortcut()
	go a.runTray()
	go a.heartbeatWatchdog()
	go a.openDashboardSoon()
	if err := a.serve(); err != nil && err != http.ErrServerClosed {
		logToFile("server error: " + err.Error())
	}
}

func singleInstance() {
	// إذا فيه نسخة قديمة شغالة على نفس المنفذ، نقفلها أولاً عشان ما تمنع نسخة التراي الجديدة.
	if current, ok := existingAgentVersion(); ok {
		if current == version {
			postExistingAgent("/api/open-dashboard")
			os.Exit(0)
		}
		postExistingAgent("/api/shutdown")
		waitExistingAgentDown(4500 * time.Millisecond)
	}

	name := utf16Ptr("XDreemB52_Device_Control_Local_Agent")
	procMutex.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if code, _, _ := procLastErr.Call(); code == errExists {
		postExistingAgent("/api/open-dashboard")
		os.Exit(0)
	}
}

func newApp() *App {
	dir := dataDir()
	a := &App{
		settingsPath: filepath.Join(dir, "settings.json"),
		mediaPath:    filepath.Join(dir, "media.json"),
		mediaDir:     filepath.Join(dir, "media"),
		events:       make([]Event, 0, 300),
		cool:         map[string]time.Time{},
		settings: Settings{
			Platform: "youtube",
			Enabled:  true,
			MediaDir: filepath.Join(dir, "media"),
			Commands: []Command{{
				ID: "demo-space", Name: "تجربة Space", Trigger: "!space", Cooldown: 1, Enabled: true,
				Effects: []Effect{{Type: "key_press", Key: "SPACE"}, {Type: "system_sound", Sound: "notify"}},
			}},
		},
	}
	_ = os.MkdirAll(a.mediaDir, 0755)
	a.loadSettings()
	a.loadMedia()
	return a
}

func dataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	p := filepath.Join(base, "DreemControl")
	_ = os.MkdirAll(p, 0755)
	return p
}

func (a *App) loadSettings() {
	b, err := os.ReadFile(a.settingsPath)
	if err != nil {
		a.saveSettings()
		return
	}
	var s Settings
	if json.Unmarshal(b, &s) == nil {
		normalizeSettings(&s)
		if s.MediaDir != "" {
			if d, ok := normalizeMediaDir(s.MediaDir); ok {
				a.mediaDir = d
				s.MediaDir = d
			}
		}
		a.settings = s
	}
}

func (a *App) saveSettings() {
	a.mu.RLock()
	s := a.settings
	a.mu.RUnlock()
	if s.MediaDir == "" {
		s.MediaDir = a.mediaDir
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(a.settingsPath, b, 0644)
}

func (a *App) loadMedia() {
	b, err := os.ReadFile(a.mediaPath)
	if err != nil {
		return
	}
	var items []MediaItem
	if json.Unmarshal(b, &items) == nil {
		for i := range items {
			if items[i].Path == "" {
				items[i].Path = filepath.Join(a.mediaDir, items[i].ID+items[i].Ext)
			}
		}
		a.media = items
	}
}

func (a *App) saveMedia() {
	a.mu.RLock()
	items := make([]MediaItem, len(a.media))
	copy(items, a.media)
	a.mu.RUnlock()
	b, _ := json.MarshalIndent(items, "", "  ")
	_ = os.WriteFile(a.mediaPath, b, 0644)
}

func normalizeSettings(s *Settings) {
	s.Platform = strings.ToLower(strings.TrimSpace(s.Platform))
	if s.Platform != "youtube" && s.Platform != "tiktok" {
		s.Platform = "youtube"
	}
	if len(s.Commands) == 0 {
		s.Commands = []Command{{ID: "demo-space", Name: "تجربة Space", Trigger: "!space", Cooldown: 1, Enabled: true, Effects: []Effect{{Type: "key_press", Key: "SPACE"}}}}
	}
	for i := range s.Commands {
		c := &s.Commands[i]
		c.ID = strings.TrimSpace(c.ID)
		if c.ID == "" {
			c.ID = "cmd-" + strconv.Itoa(i+1)
		}
		c.Trigger = strings.TrimSpace(c.Trigger)
		if c.Name == "" {
			c.Name = c.Trigger
		}
		if c.Cooldown < 0 {
			c.Cooldown = 0
		}
		if c.Cooldown > 300 {
			c.Cooldown = 300
		}
		for j := range c.Effects {
			normalizeEffect(&c.Effects[j])
		}
		if len(c.Effects) == 0 && c.Action.Type != "" {
			c.Effects = []Effect{legacyToEffect(c.Action)}
			if c.Sound != "" && c.Sound != "none" {
				c.Effects = append(c.Effects, Effect{Type: "system_sound", Sound: c.Sound})
			}
		}
	}
}

func legacyToEffect(a LegacyAction) Effect {
	t := strings.ToLower(strings.TrimSpace(a.Type))
	v := strings.ToUpper(strings.TrimSpace(a.Value))
	switch t {
	case "hotkey":
		return Effect{Type: "hotkey", Keys: splitKeys(v)}
	case "media", "media_key":
		return Effect{Type: "media_key", Key: v}
	default:
		return Effect{Type: "key_press", Key: v}
	}
}

func normalizeEffect(e *Effect) {
	e.Type = strings.ToLower(strings.TrimSpace(e.Type))
	e.Key = strings.ToUpper(strings.TrimSpace(e.Key))
	e.Value = strings.ToUpper(strings.TrimSpace(e.Value))
	e.Sound = strings.ToLower(strings.TrimSpace(e.Sound))
	e.MediaID = strings.TrimSpace(e.MediaID)
	if e.DurationMs <= 0 {
		e.DurationMs = 2500
	}
	if e.DurationMs > 30000 {
		e.DurationMs = 30000
	}
	if e.DelayMs < 0 {
		e.DelayMs = 0
	}
	if e.DelayMs > 60000 {
		e.DelayMs = 60000
	}
	if e.Width <= 0 {
		e.Width = 640
	}
	if e.Height <= 0 {
		e.Height = 360
	}
	if e.Size <= 0 {
		e.Size = 42
	}
}

func (a *App) serve() error {
	m := http.NewServeMux()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		accept := strings.ToLower(r.Header.Get("Accept"))
		if r.URL.Path == "/" && (strings.Contains(accept, "text/html") || r.Header.Get("Sec-Fetch-Dest") == "document") {
			http.Redirect(w, r, dashboard, http.StatusFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/device-control") {
			http.Redirect(w, r, dashboard, http.StatusFound)
			return
		}
		jsonOut(w, 200, map[string]any{"ok": true, "name": appName, "version": version, "dashboard": dashboard, "redirect": dashboard})
	})
	m.HandleFunc("/api/health", a.handleHealth)
	m.HandleFunc("/api/heartbeat", a.handleHeartbeat)
	m.HandleFunc("/api/dashboard/heartbeat", a.handleHeartbeat)
	m.HandleFunc("/api/settings", a.handleSettings)
	m.HandleFunc("/api/events", a.handleEvents)
	m.HandleFunc("/api/execute", a.handleExecute)
	m.HandleFunc("/api/rules/test", a.handleExecute)
	m.HandleFunc("/api/media/import", a.handleMediaImport)
	m.HandleFunc("/api/media/folder", a.handleMediaFolder)
	m.HandleFunc("/api/media/folder/pick", a.handleMediaFolderPick)
	m.HandleFunc("/api/media/folder/open", a.handleMediaFolderOpen)
	m.HandleFunc("/api/media", a.handleMedia)
	m.HandleFunc("/api/media/", a.handleMediaItem)
	m.HandleFunc("/api/displays", a.handleDisplays)
	m.HandleFunc("/api/live/connect", a.handleLiveConnect)
	m.HandleFunc("/api/live/disconnect", a.handleLiveDisconnect)
	m.HandleFunc("/api/open-dashboard", func(w http.ResponseWriter, r *http.Request) {
		openOrFocusDashboard()
		jsonOut(w, 200, map[string]any{"ok": true, "dashboard": dashboard})
	})
	m.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		// الصفحة ترسل هذا عند الإغلاق/التنقل. لا نغلق فورًا حتى لا يقتل bootstrap الصفحة أثناء إعادة كتابة الـHTML.
		atomic.StoreUint32(&a.heartbeatSeen, 1)
		atomic.StoreInt64(&a.lastHeartbeat, time.Now().Add(-16*time.Second).UnixMilli())
		a.emit(Event{Type: "status", Status: "page-close", Text: "وصل طلب إغلاق صفحة الاستديو؛ إذا لم ترجع الصفحة خلال ثواني سيقفل البرنامج"})
		jsonOut(w, 200, map[string]any{"ok": true, "closing": "delayed"})
	})
	a.srv = &http.Server{Addr: addr, Handler: a.localOnly(cors(m)), ReadHeaderTimeout: 5 * time.Second}
	a.emit(Event{Type: "status", Status: "ready", Text: appName + " يعمل بالخلفية على " + addr})
	return a.srv.ListenAndServe()
}

func (a *App) localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			h = r.RemoteAddr
		}
		ip := net.ParseIP(h)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "local only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	a.markPageSeen()
	jsonOut(w, 200, map[string]any{
		"ok": true, "name": appName, "version": version, "local": true,
		"dashboard":    dashboard,
		"capabilities": map[string]bool{"keyboard": true, "hotkey": true, "media_key": true, "sound": true, "media_local": true, "media_dir_picker": true, "overlay": true, "youtube_local": true, "tiktok_local": true},
	})
}

func (a *App) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	now := a.markPageSeen()
	jsonOut(w, 200, map[string]any{"ok": true, "name": appName, "version": version, "time": now, "autoClose": "page"})
}

func (a *App) markPageSeen() int64 {
	now := time.Now().UnixMilli()
	atomic.StoreInt64(&a.lastHeartbeat, now)
	atomic.StoreUint32(&a.heartbeatSeen, 1)
	return now
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.markPageSeen()
		a.mu.RLock()
		s := a.settings
		a.mu.RUnlock()
		jsonOut(w, 200, s)
	case http.MethodPost:
		var s Settings
		if err := decodeJSON(r, &s, maxSettings); err != nil {
			jsonOut(w, 400, map[string]any{"error": "INVALID_SETTINGS"})
			return
		}
		normalizeSettings(&s)
		if strings.TrimSpace(s.MediaDir) == "" {
			a.mu.RLock()
			s.MediaDir = a.mediaDir
			a.mu.RUnlock()
		}
		if len(s.Commands) > 100 {
			jsonOut(w, 400, map[string]any{"error": "TOO_MANY_COMMANDS"})
			return
		}
		a.mu.Lock()
		if nd, ok := normalizeMediaDir(s.MediaDir); ok {
			a.mediaDir = nd
			s.MediaDir = nd
		}
		a.settings = s
		a.mu.Unlock()
		a.saveSettings()
		a.emit(Event{Type: "settings", Status: "saved", Text: "تم حفظ إعدادات التحكم"})
		jsonOut(w, 200, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	a.mu.RLock()
	out := make([]Event, 0, 100)
	for _, e := range a.events {
		if e.Seq > after {
			out = append(out, e)
			if len(out) >= 100 {
				break
			}
		}
	}
	a.mu.RUnlock()
	jsonOut(w, 200, map[string]any{"ok": true, "events": out})
}

func (a *App) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var p struct {
		Action  LegacyAction `json:"action"`
		Effect  Effect       `json:"effect"`
		Effects []Effect     `json:"effects"`
		Sound   string       `json:"sound"`
	}
	if err := decodeJSON(r, &p, maxSettings); err != nil {
		jsonOut(w, 400, map[string]any{"error": "INVALID"})
		return
	}
	effects := p.Effects
	if len(effects) == 0 && p.Effect.Type != "" {
		effects = []Effect{p.Effect}
	}
	if len(effects) == 0 && p.Action.Type != "" {
		effects = []Effect{legacyToEffect(p.Action)}
		if p.Sound != "" && p.Sound != "none" {
			effects = append(effects, Effect{Type: "system_sound", Sound: p.Sound})
		}
	}
	if err := a.runEffects(effects); err != nil {
		a.emit(Event{Type: "exec", Status: "error", Text: "اختبار: " + err.Error()})
		jsonOut(w, 400, map[string]any{"error": err.Error()})
		return
	}
	a.emit(Event{Type: "exec", Status: "ok", Text: "تم اختبار التأثير بنجاح"})
	jsonOut(w, 200, map[string]any{"ok": true})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		if o == origin || o == "http://127.0.0.1:17836" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodOptions {
			if o != "" && o != origin && o != "http://127.0.0.1:17836" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && o != "" && o != origin && o != "http://127.0.0.1:17836" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(r *http.Request, v any, max int64) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, max)).Decode(v)
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------- media folders/files ----------------
func normalizeMediaDir(input string) (string, bool) {
	p := strings.TrimSpace(input)
	if p == "" {
		return "", false
	}
	p = os.ExpandEnv(p)
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return "", false
	}
	probe := filepath.Join(abs, ".xd-device-control-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0644); err != nil {
		return "", false
	}
	_ = os.Remove(probe)
	return abs, true
}

func mediaDirLabel(p string) string {
	p = strings.TrimSpace(filepath.Clean(p))
	if p == "." || p == "" {
		return "مجلد الوسائط المحلي"
	}
	base := filepath.Base(p)
	parent := filepath.Base(filepath.Dir(p))
	if parent != "." && parent != string(filepath.Separator) && parent != "" {
		return parent + string(filepath.Separator) + base
	}
	return base
}

func (a *App) handleMediaFolder(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		d := a.mediaDir
		a.mu.RUnlock()
		jsonOut(w, 200, map[string]any{"ok": true, "label": mediaDirLabel(d), "custom": true})
	case http.MethodPost:
		var p struct {
			Path string `json:"path"`
		}
		if err := decodeJSON(r, &p, maxSettings); err != nil {
			jsonOut(w, 400, map[string]any{"error": "INVALID"})
			return
		}
		d, ok := normalizeMediaDir(p.Path)
		if !ok {
			jsonOut(w, 400, map[string]any{"error": "تعذر استخدام هذا المجلد"})
			return
		}
		a.mu.Lock()
		a.mediaDir = d
		a.settings.MediaDir = d
		a.mu.Unlock()
		a.saveSettings()
		a.emit(Event{Type: "media", Status: "folder", Text: "تم اختيار مجلد وسائط محلي: " + mediaDirLabel(d)})
		jsonOut(w, 200, map[string]any{"ok": true, "label": mediaDirLabel(d)})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleMediaFolderPick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	script := `$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Windows.Forms; [Console]::OutputEncoding=[Text.UTF8Encoding]::UTF8; [System.Windows.Forms.Application]::EnableVisualStyles(); $dlg=New-Object System.Windows.Forms.FolderBrowserDialog; $dlg.Description='اختر مجلد وسائط التحكم في الجهاز'; $dlg.ShowNewFolderButton=$true; if($dlg.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK){[Console]::Out.Write($dlg.SelectedPath)}`
	out, err := runPSOutput(script)
	if err != nil {
		jsonOut(w, 500, map[string]any{"error": "تعذر فتح اختيار المجلد"})
		return
	}
	d, ok := normalizeMediaDir(strings.TrimSpace(string(out)))
	if !ok {
		jsonOut(w, 400, map[string]any{"error": "لم يتم اختيار مجلد"})
		return
	}
	a.mu.Lock()
	a.mediaDir = d
	a.settings.MediaDir = d
	a.mu.Unlock()
	a.saveSettings()
	a.emit(Event{Type: "media", Status: "folder", Text: "تم اختيار مجلد وسائط محلي: " + mediaDirLabel(d)})
	jsonOut(w, 200, map[string]any{"ok": true, "label": mediaDirLabel(d)})
}

func (a *App) handleMediaFolderOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.mu.RLock()
	d := a.mediaDir
	a.mu.RUnlock()
	_ = os.MkdirAll(d, 0755)
	procShell.Call(0, uintptr(unsafe.Pointer(utf16Ptr("open"))), uintptr(unsafe.Pointer(utf16Ptr(d))), 0, 0, showNormal)
	jsonOut(w, 200, map[string]any{"ok": true, "label": mediaDirLabel(d)})
}

func (a *App) handleMediaImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 300<<20)
	if err := r.ParseMultipartForm(300 << 20); err != nil {
		jsonOut(w, 400, map[string]any{"error": "INVALID_UPLOAD"})
		return
	}
	file, hdr, err := firstFile(r.MultipartForm)
	if err != nil {
		jsonOut(w, 400, map[string]any{"error": "NO_FILE"})
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	kind := kindForExt(ext)
	if kind == "" {
		jsonOut(w, 400, map[string]any{"error": "نوع الملف غير مدعوم"})
		return
	}
	id := randID()
	a.mu.RLock()
	mediaDir := a.mediaDir
	a.mu.RUnlock()
	_ = os.MkdirAll(mediaDir, 0755)
	dst := filepath.Join(mediaDir, id+ext)
	out, err := os.Create(dst)
	if err != nil {
		jsonOut(w, 500, map[string]any{"error": "تعذر حفظ الملف"})
		return
	}
	n, err := io.Copy(out, file)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(dst)
		jsonOut(w, 500, map[string]any{"error": "فشل حفظ الملف"})
		return
	}
	item := MediaItem{ID: id, Name: hdr.Filename, Kind: kind, Ext: ext, Size: n, CreatedAt: time.Now().UnixMilli(), Path: dst}
	a.mu.Lock()
	a.media = append(a.media, item)
	a.mu.Unlock()
	a.saveMedia()
	a.emit(Event{Type: "media", Status: "import", Text: "تم حفظ وسيط محلي: " + hdr.Filename})
	jsonOut(w, 200, map[string]any{"ok": true, "media": item})
}

func kindForExt(ext string) string {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp":
		return "image"
	case ".mp4", ".webm", ".mov", ".mkv", ".avi":
		return "video"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac":
		return "audio"
	default:
		return ""
	}
}

func (a *App) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	a.mu.RLock()
	items := make([]MediaItem, len(a.media))
	copy(items, a.media)
	a.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	jsonOut(w, 200, map[string]any{"ok": true, "media": items})
}

func (a *App) handleMediaItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/media/")
	if id == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodDelete {
		a.mu.Lock()
		idx := -1
		var item MediaItem
		for i, m := range a.media {
			if m.ID == id {
				idx = i
				item = m
				break
			}
		}
		if idx >= 0 {
			a.media = append(a.media[:idx], a.media[idx+1:]...)
		}
		a.mu.Unlock()
		if idx < 0 {
			jsonOut(w, 404, map[string]any{"error": "NOT_FOUND"})
			return
		}
		if item.Path != "" {
			_ = os.Remove(item.Path)
		}
		a.saveMedia()
		jsonOut(w, 200, map[string]any{"ok": true})
		return
	}
	if r.Method == http.MethodGet {
		p, err := a.mediaFileAny(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (a *App) mediaFileAny(id string) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, m := range a.media {
		if m.ID == id && m.Path != "" {
			if _, err := os.Stat(m.Path); err == nil {
				return m.Path, nil
			}
		}
	}
	return "", errors.New("media not found")
}

func (a *App) mediaFile(id, want string) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, m := range a.media {
		if m.ID == id {
			if want != "" && m.Kind != want {
				return "", fmt.Errorf("الوسيط ليس %s", want)
			}
			if _, err := os.Stat(m.Path); err != nil {
				return "", errors.New("ملف الوسيط غير موجود")
			}
			return m.Path, nil
		}
	}
	return "", errors.New("mediaId غير موجود")
}

func (a *App) handleDisplays(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ds := getDisplays()
	jsonOut(w, 200, map[string]any{"ok": true, "displays": ds})
}

func (a *App) handleLiveConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var p struct {
		Platform string `json:"platform"`
		Source   string `json:"source"`
	}
	if err := decodeJSON(r, &p, maxSettings); err != nil {
		jsonOut(w, 400, map[string]any{"error": "INVALID"})
		return
	}
	p.Platform = strings.ToLower(strings.TrimSpace(p.Platform))
	p.Source = strings.TrimSpace(p.Source)
	if p.Platform == "" {
		p.Platform = "youtube"
	}
	if p.Source == "" || strings.EqualFold(p.Source, "auto") || strings.EqualFold(p.Source, "official") {
		if platform, source, err := officialLiveSource(); err == nil {
			p.Platform, p.Source = platform, source
		} else {
			jsonOut(w, 400, map[string]any{"error": "ما لقيت بث شغال من الموقع الرسمي. حط رابط البث يدويًا."})
			return
		}
	}
	a.stopLive()
	a.mu.Lock()
	a.settings.Platform = p.Platform
	a.settings.Source = p.Source
	a.mu.Unlock()
	a.saveSettings()
	switch p.Platform {
	case "youtube":
		ctx, cancel := context.WithCancel(context.Background())
		a.mu.Lock()
		a.ytCancel = cancel
		a.mu.Unlock()
		go a.youtubeLoop(ctx, p.Source)
		jsonOut(w, 200, map[string]any{"ok": true, "mode": "local", "platform": "youtube"})
	case "tiktok":
		ctx, cancel := context.WithCancel(context.Background())
		a.mu.Lock()
		a.ttCancel = cancel
		a.mu.Unlock()
		go a.tiktokLoop(ctx, p.Source)
		jsonOut(w, 200, map[string]any{"ok": true, "mode": "local", "platform": "tiktok"})
	default:
		jsonOut(w, 400, map[string]any{"error": "INVALID_PLATFORM"})
	}
}

func (a *App) handleLiveDisconnect(w http.ResponseWriter, r *http.Request) {
	a.stopLive()
	a.emit(Event{Type: "status", Status: "disconnected", Text: "تم فصل الدردشة"})
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (a *App) stopLive() {
	a.mu.Lock()
	if a.ytCancel != nil {
		a.ytCancel()
		a.ytCancel = nil
	}
	if a.ttCancel != nil {
		a.ttCancel()
		a.ttCancel = nil
	}
	a.mu.Unlock()
}

// ---------------- execution ----------------
func (a *App) chat(platform, user, text, id string) {
	a.emit(Event{Type: "chat", Platform: platform, User: user, Text: text, ID: id})
	a.mu.RLock()
	s := a.settings
	a.mu.RUnlock()
	if !s.Enabled {
		return
	}
	msg := strings.TrimSpace(text)
	for _, c := range s.Commands {
		if !c.Enabled || c.Trigger == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(c.Trigger), msg) {
			continue
		}
		now := time.Now()
		a.mu.Lock()
		until := a.cool[c.ID]
		if now.Before(until) {
			a.mu.Unlock()
			continue
		}
		a.cool[c.ID] = now.Add(time.Duration(c.Cooldown * float64(time.Second)))
		a.mu.Unlock()
		effects := c.Effects
		if len(effects) == 0 && c.Action.Type != "" {
			effects = []Effect{legacyToEffect(c.Action)}
		}
		if err := a.runEffects(effects); err != nil {
			a.emit(Event{Type: "exec", Status: "error", Text: err.Error()})
		} else {
			a.emit(Event{Type: "exec", Status: "ok", Platform: platform, User: user, Text: "نفذ " + c.Name + " بواسطة " + user})
		}
	}
}

func (a *App) emit(e Event) {
	if e.Time == 0 {
		e.Time = time.Now().UnixMilli()
	}
	e.Seq = atomic.AddUint64(&a.seq, 1)
	a.mu.Lock()
	a.events = append(a.events, e)
	if len(a.events) > 300 {
		a.events = append([]Event(nil), a.events[len(a.events)-220:]...)
	}
	a.mu.Unlock()
}

func officialLiveSource() (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", origin+"/api/site/live", nil)
	res, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return "", "", fmt.Errorf("official live HTTP %d", res.StatusCode)
	}
	var d struct {
		Live     bool   `json:"live"`
		Platform string `json:"platform"`
		URL      string `json:"url"`
		Source   string `json:"source"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&d); err != nil {
		return "", "", err
	}
	platform := strings.ToLower(strings.TrimSpace(d.Platform))
	source := strings.TrimSpace(d.URL)
	if source == "" {
		source = strings.TrimSpace(d.Source)
	}
	if platform != "youtube" && platform != "tiktok" {
		platform = "youtube"
	}
	if !d.Live || source == "" {
		return "", "", errors.New("no official live source")
	}
	return platform, source, nil
}

// ---------------- TikTok live chat through the local reader used by earlier builds ----------------
func cleanTikTokHandle(src string) string {
	s := strings.TrimSpace(src)
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		if m := regexp.MustCompile(`(?i)/@([A-Za-z0-9._]{2,24})`).FindStringSubmatch(u.Path); len(m) > 1 {
			return m[1]
		}
	}
	s = strings.TrimLeft(s, "@")
	s = strings.Split(strings.Split(s, "/")[0], "?")[0]
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9._]{2,24}$`, s); ok {
		return s
	}
	return ""
}

func (a *App) tiktokReaderPath() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(base, "Dreem-TikTok-Reader.exe"), filepath.Join(base, "tiktok-reader.exe"))
	}
	candidates = append(candidates, filepath.Join(dataDir(), "Dreem-TikTok-Reader.exe"), filepath.Join(dataDir(), "tiktok-reader.exe"))
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.Size() > 5*1024*1024 {
			return c, nil
		}
	}
	// Earlier published builds expose the reader separately on the official site. Cache it locally.
	dst := filepath.Join(dataDir(), "Dreem-TikTok-Reader.exe")
	url := origin + "/downloads/Dreem-TikTok-Reader.exe"
	res, err := httpClient.Get(url)
	if err != nil {
		return "", errors.New("تعذر تحميل قارئ TikTok المحلي من الموقع الرسمي")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return "", fmt.Errorf("تعذر تحميل قارئ TikTok: HTTP %d", res.StatusCode)
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, io.LimitReader(res.Body, 80<<20))
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if n < 5*1024*1024 {
		_ = os.Remove(tmp)
		return "", errors.New("قارئ TikTok المحمّل غير مكتمل")
	}
	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func (a *App) tiktokLoop(ctx context.Context, src string) {
	h := cleanTikTokHandle(src)
	if h == "" {
		a.emit(Event{Type: "status", Platform: "tiktok", Status: "error", Text: "اسم TikTok غير صحيح"})
		return
	}
	a.emit(Event{Type: "status", Platform: "tiktok", Status: "connecting", Text: "جاري تجهيز قارئ TikTok المحلي..."})
	p, err := a.tiktokReaderPath()
	if err != nil {
		a.emit(Event{Type: "status", Platform: "tiktok", Status: "error", Text: err.Error()})
		return
	}
	cmd := hiddenCommandContext(ctx, p, h)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.emit(Event{Type: "status", Platform: "tiktok", Status: "error", Text: err.Error()})
		return
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		a.emit(Event{Type: "status", Platform: "tiktok", Status: "error", Text: err.Error()})
		return
	}
	a.emit(Event{Type: "status", Platform: "tiktok", Status: "connected", Text: "تم ربط TikTok Live عبر أداة التحكم"})
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 4096), 1024*1024)
		for sc.Scan() {
			t := strings.TrimSpace(sc.Text())
			if t != "" {
				a.emit(Event{Type: "status", Platform: "tiktok", Status: "warning", Text: t})
			}
		}
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 4096), 1024*1024)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var x struct{ Type, Status, Text, User, ID string }
		if json.Unmarshal(sc.Bytes(), &x) != nil {
			continue
		}
		if x.Type == "chat" {
			a.chat("tiktok", x.User, x.Text, x.ID)
		} else if x.Type == "status" {
			a.emit(Event{Type: "status", Platform: "tiktok", Status: x.Status, Text: x.Text})
		}
	}
	if err := cmd.Wait(); err != nil {
		select {
		case <-ctx.Done():
			return
		default:
			a.emit(Event{Type: "status", Platform: "tiktok", Status: "error", Text: "انقطع TikTok: " + err.Error()})
		}
	}
}

// ---------------- YouTube live chat, lightweight public polling ----------------
func (a *App) youtubeLoop(ctx context.Context, src string) {
	a.emit(Event{Type: "status", Platform: "youtube", Status: "connecting", Text: "جاري ربط YouTube بنفس مصدر البث الرسمي..."})
	video, err := resolveYouTubeVideo(src)
	if err != nil {
		a.emit(Event{Type: "status", Platform: "youtube", Status: "error", Text: err.Error()})
		return
	}
	cont, key, err := liveContinuation(video)
	if err != nil {
		a.emit(Event{Type: "status", Platform: "youtube", Status: "error", Text: err.Error()})
		return
	}
	a.emit(Event{Type: "status", Platform: "youtube", Status: "connected", Text: "تم ربط YouTube Live عبر أداة التحكم"})
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		next, wait, msgs, err := fetchLiveChat(key, cont)
		if err != nil {
			a.emit(Event{Type: "status", Platform: "youtube", Status: "warning", Text: "تعذر قراءة الشات: " + err.Error()})
			time.Sleep(4 * time.Second)
			continue
		}
		for _, m := range msgs {
			if m.ID != "" && seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			if strings.TrimSpace(m.Text) != "" {
				a.chat("youtube", m.User, m.Text, m.ID)
			}
		}
		if next != "" {
			cont = next
		}
		if wait < 1500 {
			wait = 2500
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(wait) * time.Millisecond):
		}
	}
}

type ytMsg struct{ ID, User, Text string }

var httpClient = &http.Client{Timeout: 18 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 6 {
		return http.ErrUseLastResponse
	}
	return nil
}}

func resolveYouTubeVideo(src string) (string, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return "", errors.New("رابط YouTube فارغ")
	}
	if m := regexp.MustCompile(`(?i)(?:v=|youtu\.be/|/shorts/|/live/)([A-Za-z0-9_-]{6,})`).FindStringSubmatch(s); len(m) > 1 {
		return m[1], nil
	}
	if strings.HasPrefix(s, "@") || !strings.Contains(s, "/") {
		s = "https://www.youtube.com/" + strings.TrimPrefix(s, "@")
	}
	if !strings.HasPrefix(s, "http") {
		s = "https://www.youtube.com/" + strings.TrimLeft(s, "/")
	}
	candidates := []string{s, strings.TrimRight(s, "/") + "/live"}
	for _, u := range candidates {
		id, err := youtubeLiveIDFromURL(u)
		if err == nil && id != "" {
			return id, nil
		}
	}
	return "", errors.New("ما قدرت ألقى بث YouTube مباشر من الرابط")
}

func youtubeLiveIDFromURL(u string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	res, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	t := string(b)
	patterns := []string{`"videoId":"([A-Za-z0-9_-]{6,})"`, `"liveVideoId":"([A-Za-z0-9_-]{6,})"`, `watch\?v=([A-Za-z0-9_-]{6,})`}
	for _, p := range patterns {
		if m := regexp.MustCompile(p).FindStringSubmatch(t); len(m) > 1 {
			return m[1], nil
		}
	}
	return "", errors.New("no live video id")
}

func liveContinuation(video string) (string, string, error) {
	u := "https://www.youtube.com/live_chat?is_popout=1&v=" + url.QueryEscape(video)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	res, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	t := string(b)
	key := ""
	if m := regexp.MustCompile(`"INNERTUBE_API_KEY":"([^"]+)"`).FindStringSubmatch(t); len(m) > 1 {
		key = m[1]
	}
	cont := ""
	for _, p := range []string{`"continuation":"([^"]+)"`, `continuation=([^"&]+)`} {
		if m := regexp.MustCompile(p).FindStringSubmatch(t); len(m) > 1 {
			cont = strings.ReplaceAll(m[1], `\\u0026`, "&")
			break
		}
	}
	if key == "" || cont == "" {
		return "", "", errors.New("الشات غير متاح أو البث غير مباشر")
	}
	return cont, key, nil
}

func fetchLiveChat(key, cont string) (string, int, []ytMsg, error) {
	payload := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": "2.20240220.00.00"}}, "continuation": cont}
	b, _ := json.Marshal(payload)
	u := "https://www.youtube.com/youtubei/v1/live_chat/get_live_chat?key=" + url.QueryEscape(key)
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	res, err := httpClient.Do(req)
	if err != nil {
		return cont, 3500, nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 5<<20))
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return cont, 4000, nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil {
		return cont, 4000, nil, errors.New("رد غير مفهوم")
	}
	next := findString(raw, "continuation")
	wait := int(findNumber(raw, "timeoutMs"))
	msgs := extractYouTubeMessages(raw)
	return next, wait, msgs, nil
}

func extractYouTubeMessages(v any) []ytMsg {
	out := []ytMsg{}
	var walk func(any)
	walk = func(x any) {
		switch z := x.(type) {
		case map[string]any:
			if r, ok := z["liveChatTextMessageRenderer"].(map[string]any); ok {
				id := strAny(r["id"])
				user := runsText(r["authorName"])
				text := runsText(r["message"])
				out = append(out, ytMsg{ID: id, User: user, Text: text})
			}
			for _, vv := range z {
				walk(vv)
			}
		case []any:
			for _, vv := range z {
				walk(vv)
			}
		}
	}
	walk(v)
	return out
}

func runsText(v any) string {
	m, _ := v.(map[string]any)
	arr, _ := m["runs"].([]any)
	parts := []string{}
	for _, x := range arr {
		if mm, ok := x.(map[string]any); ok {
			parts = append(parts, strAny(mm["text"]))
		}
	}
	if len(parts) == 0 {
		return strAny(m["simpleText"])
	}
	return strings.Join(parts, "")
}
func strAny(v any) string { return strings.TrimSpace(fmt.Sprint(v)) }
func findString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if val, ok := x[key]; ok {
			s := strings.TrimSpace(fmt.Sprint(val))
			if s != "" && s != "<nil>" {
				return s
			}
		}
		for _, vv := range x {
			if s := findString(vv, key); s != "" {
				return s
			}
		}
	case []any:
		for _, vv := range x {
			if s := findString(vv, key); s != "" {
				return s
			}
		}
	}
	return ""
}
func findNumber(v any, key string) float64 {
	switch x := v.(type) {
	case map[string]any:
		if val, ok := x[key]; ok {
			switch n := val.(type) {
			case float64:
				return n
			case int:
				return float64(n)
			}
		}
		for _, vv := range x {
			if n := findNumber(vv, key); n != 0 {
				return n
			}
		}
	case []any:
		for _, vv := range x {
			if n := findNumber(vv, key); n != 0 {
				return n
			}
		}
	}
	return 0
}

// ---------------- action implementation ----------------
var keyMap = map[string]byte{
	"SPACE": 0x20, "ENTER": 0x0D, "TAB": 0x09, "ESC": 0x1B, "ESCAPE": 0x1B, "BACKSPACE": 0x08,
	"DELETE": 0x2E, "INSERT": 0x2D, "HOME": 0x24, "END": 0x23, "PAGEUP": 0x21, "PAGEDOWN": 0x22,
	"UP": 0x26, "DOWN": 0x28, "LEFT": 0x25, "RIGHT": 0x27,
	"SHIFT": 0x10, "CTRL": 0x11, "CONTROL": 0x11, "ALT": 0x12, "WIN": 0x5B,
	"F1": 0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73, "F5": 0x74, "F6": 0x75, "F7": 0x76, "F8": 0x77, "F9": 0x78, "F10": 0x79, "F11": 0x7A, "F12": 0x7B,
	"F13": 0x7C, "F14": 0x7D, "F15": 0x7E, "F16": 0x7F, "F17": 0x80, "F18": 0x81, "F19": 0x82, "F20": 0x83, "F21": 0x84, "F22": 0x85, "F23": 0x86, "F24": 0x87,
	"VOLUME_MUTE": 0xAD, "VOLUME_DOWN": 0xAE, "VOLUME_UP": 0xAF, "MEDIA_NEXT": 0xB0, "MEDIA_PREV": 0xB1, "MEDIA_STOP": 0xB2, "MEDIA_PLAY_PAUSE": 0xB3,
}

func init() {
	for ch := 'A'; ch <= 'Z'; ch++ {
		keyMap[string(ch)] = byte(ch)
	}
	for ch := '0'; ch <= '9'; ch++ {
		keyMap[string(ch)] = byte(ch)
	}
}

func splitKeys(v string) []string {
	v = strings.ReplaceAll(v, "+", ",")
	parts := strings.Split(v, ",")
	out := []string{}
	for _, p := range parts {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func keyCode(k string) (byte, bool) { v, ok := keyMap[strings.ToUpper(strings.TrimSpace(k))]; return v, ok }
func tapKey(k string) error {
	vk, ok := keyCode(k)
	if !ok {
		return fmt.Errorf("زر غير مدعوم: %s", k)
	}
	procKeybd.Call(uintptr(vk), 0, 0, 0)
	time.Sleep(35 * time.Millisecond)
	procKeybd.Call(uintptr(vk), 0, keyUp, 0)
	return nil
}
func hotkey(keys []string) error {
	if len(keys) == 0 {
		return errors.New("Hotkey فارغ")
	}
	vks := []byte{}
	for _, k := range keys {
		vk, ok := keyCode(k)
		if !ok {
			return fmt.Errorf("زر غير مدعوم: %s", k)
		}
		vks = append(vks, vk)
	}
	for _, vk := range vks {
		procKeybd.Call(uintptr(vk), 0, 0, 0)
		time.Sleep(18 * time.Millisecond)
	}
	for i := len(vks) - 1; i >= 0; i-- {
		procKeybd.Call(uintptr(vks[i]), 0, keyUp, 0)
		time.Sleep(18 * time.Millisecond)
	}
	return nil
}
func systemSound(name string) error {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "none":
		return nil
	case "notify", "asterisk":
		procBeep.Call(0x40)
	case "exclamation", "warning":
		procBeep.Call(0x30)
	case "hand", "error":
		procBeep.Call(0x10)
	case "beep":
		procBeep.Call(0xFFFFFFFF)
	default:
		return errors.New("صوت نظام غير معروف")
	}
	return nil
}
func playFile(p string) error {
	if p == "" {
		return errors.New("ملف الصوت غير موجود")
	}
	procPlay.Call(uintptr(unsafe.Pointer(utf16Ptr(p))), 0, sndFile|sndAsync|sndNoDef)
	return nil
}

func (a *App) runEffects(effects []Effect) error {
	if len(effects) == 0 {
		return errors.New("لا يوجد Effects")
	}
	for _, e := range effects {
		normalizeEffect(&e)
		if err := a.runEffect(e); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) runEffect(e Effect) error {
	switch e.Type {
	case "key", "key_press":
		k := e.Key
		if k == "" {
			k = e.Value
		}
		return tapKey(k)
	case "hotkey":
		keys := e.Keys
		if len(keys) == 0 && e.Value != "" {
			keys = splitKeys(e.Value)
		}
		return hotkey(keys)
	case "media_key":
		k := e.Key
		if k == "" {
			k = e.Value
		}
		return tapKey(k)
	case "system_sound":
		return systemSound(e.Sound)
	case "audio_file":
		p, err := a.mediaFile(e.MediaID, "audio")
		if err != nil {
			return err
		}
		return playFile(p)
	case "image", "video", "text_overlay":
		return a.showOverlay(e)
	case "delay":
		time.Sleep(time.Duration(e.DelayMs) * time.Millisecond)
		return nil
	default:
		return fmt.Errorf("effect غير معروف: %s", e.Type)
	}
}

type monitorRect struct{ Left, Top, Right, Bottom int32 }
type monitorInfoEx struct {
	CbSize    uint32
	RcMonitor monitorRect
	RcWork    monitorRect
	DwFlags   uint32
	SzDevice  [32]uint16
}

func getDisplays() []Display {
	out := make([]Display, 0, 4)
	cb := syscall.NewCallback(func(hMonitor, hdcMonitor, lprcMonitor, dwData uintptr) uintptr {
		var mi monitorInfoEx
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		ok, _, _ := procGetMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
		if ok == 0 {
			return 1
		}
		name := syscall.UTF16ToString(mi.SzDevice[:])
		if name == "" {
			name = fmt.Sprintf("Screen %d", len(out)+1)
		}
		d := Display{
			ID:      name,
			Name:    name,
			Primary: mi.DwFlags&monitorPrimary != 0,
			X:       int(mi.RcMonitor.Left),
			Y:       int(mi.RcMonitor.Top),
			Width:   int(mi.RcMonitor.Right - mi.RcMonitor.Left),
			Height:  int(mi.RcMonitor.Bottom - mi.RcMonitor.Top),
		}
		if d.Width > 0 && d.Height > 0 {
			out = append(out, d)
		}
		return 1
	})
	ok, _, _ := procEnumDisplayMonitors.Call(0, 0, cb, 0)
	if ok != 0 && len(out) > 0 {
		primary := 0
		for i, d := range out {
			if d.Primary {
				primary = i
				break
			}
		}
		if primary != 0 {
			out[0], out[primary] = out[primary], out[0]
		}
		return out
	}
	w, _, _ := procGetSystemMetrics.Call(smCxScreen)
	h, _, _ := procGetSystemMetrics.Call(smCyScreen)
	if w == 0 {
		w = 1920
	}
	if h == 0 {
		h = 1080
	}
	return []Display{{ID: "primary", Name: "Primary", Primary: true, X: 0, Y: 0, Width: int(w), Height: int(h)}}
}

func (a *App) showOverlay(e Effect) error {
	ds := getDisplays()
	d := ds[0]
	for _, x := range ds {
		if e.DisplayID != "" && x.ID == e.DisplayID {
			d = x
			break
		}
		if x.Primary {
			d = x
		}
	}
	x, y, w, h := overlayRect(e, d)
	dur := e.DurationMs
	if dur <= 0 {
		dur = 2500
	}
	switch e.Type {
	case "text_overlay":
		text := strings.ReplaceAll(e.Text, "'", "''")
		script := fmt.Sprintf(`Add-Type -AssemblyName PresentationFramework; $w=New-Object Windows.Window; $w.WindowStyle='None'; $w.AllowsTransparency=$true; $w.Background='Transparent'; $w.Topmost=$true; $w.ShowInTaskbar=$false; $w.Left=%d; $w.Top=%d; $w.Width=%d; $w.Height=%d; $tb=New-Object Windows.Controls.TextBlock; $tb.Text='%s'; $tb.Foreground='White'; $tb.FontSize=%d; $tb.FontWeight='Bold'; $tb.TextAlignment='Center'; $tb.VerticalAlignment='Center'; $tb.HorizontalAlignment='Center'; $tb.TextWrapping='Wrap'; $border=New-Object Windows.Controls.Border; $border.Background='#CC111111'; $border.CornerRadius='18'; $border.Padding='20'; $border.Child=$tb; $w.Content=$border; $timer=New-Object Windows.Threading.DispatcherTimer; $timer.Interval=[TimeSpan]::FromMilliseconds(%d); $timer.Add_Tick({$timer.Stop();$w.Close()}); $timer.Start(); $w.ShowDialog()|Out-Null`, x, y, w, h, text, e.Size, dur)
		return startPS(script)
	case "image", "video":
		p, err := a.mediaFile(e.MediaID, map[bool]string{true: "image", false: "video"}[e.Type == "image"])
		if err != nil {
			return err
		}
		p = strings.ReplaceAll(p, "'", "''")
		var script string
		if e.Type == "image" {
			script = fmt.Sprintf(`Add-Type -AssemblyName PresentationFramework; $w=New-Object Windows.Window; $w.WindowStyle='None'; $w.AllowsTransparency=$true; $w.Background='Transparent'; $w.Topmost=$true; $w.ShowInTaskbar=$false; $w.Left=%d; $w.Top=%d; $w.Width=%d; $w.Height=%d; $img=New-Object Windows.Controls.Image; $img.Source=[Windows.Media.Imaging.BitmapImage]::new([Uri]'%s'); $img.Stretch='Uniform'; $w.Content=$img; $timer=New-Object Windows.Threading.DispatcherTimer; $timer.Interval=[TimeSpan]::FromMilliseconds(%d); $timer.Add_Tick({$timer.Stop();$w.Close()}); $timer.Start(); $w.ShowDialog()|Out-Null`, x, y, w, h, p, dur)
		} else {
			script = fmt.Sprintf(`Add-Type -AssemblyName PresentationFramework; $w=New-Object Windows.Window; $w.WindowStyle='None'; $w.AllowsTransparency=$true; $w.Background='Transparent'; $w.Topmost=$true; $w.ShowInTaskbar=$false; $w.Left=%d; $w.Top=%d; $w.Width=%d; $w.Height=%d; $m=New-Object Windows.Controls.MediaElement; $m.Source=[Uri]'%s'; $m.LoadedBehavior='Manual'; $m.UnloadedBehavior='Stop'; $m.Stretch='Uniform'; $w.Content=$m; $m.Add_MediaOpened({$m.Play()}); $timer=New-Object Windows.Threading.DispatcherTimer; $timer.Interval=[TimeSpan]::FromMilliseconds(%d); $timer.Add_Tick({$timer.Stop();$m.Stop();$w.Close()}); $timer.Start(); $w.ShowDialog()|Out-Null`, x, y, w, h, p, dur)
		}
		return startPS(script)
	}
	return nil
}
func overlayRect(e Effect, d Display) (int, int, int, int) {
	w, h := e.Width, e.Height
	if w <= 0 {
		w = 640
	}
	if h <= 0 {
		h = 360
	}
	x, y := e.X, e.Y
	pos := strings.ToLower(e.Position)
	switch pos {
	case "center", "":
		x = d.X + (d.Width-w)/2
		y = d.Y + (d.Height-h)/2
	case "top":
		x = d.X + (d.Width-w)/2
		y = d.Y + 40
	case "bottom":
		x = d.X + (d.Width-w)/2
		y = d.Y + d.Height - h - 60
	case "left":
		x = d.X + 40
		y = d.Y + (d.Height-h)/2
	case "right":
		x = d.X + d.Width - w - 40
		y = d.Y + (d.Height-h)/2
	}
	return x, y, w, h
}
func startPS(script string) error {
	cmd := hiddenCommand("powershell.exe", "-NoProfile", "-Sta", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-EncodedCommand", psEncoded(script))
	return cmd.Start()
}
func runPSOutput(script string) ([]byte, error) {
	cmd := hiddenCommand("powershell.exe", "-NoProfile", "-Sta", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-EncodedCommand", psEncoded(script))
	return cmd.Output()
}
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}
func hiddenCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}
func psEncoded(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		b[i*2] = byte(v)
		b[i*2+1] = byte(v >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// ---------------- utils ----------------
func firstFile(f *multipart.Form) (multipart.File, *multipart.FileHeader, error) {
	if f == nil {
		return nil, nil, errors.New("no form")
	}
	for _, hs := range f.File {
		if len(hs) > 0 {
			file, err := hs[0].Open()
			return file, hs[0], err
		}
	}
	return nil, nil, errors.New("no file")
}
func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("m%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct{ X, Y int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type notifyIconData struct {
	CbSize            uint32
	HWnd              uintptr
	UID               uint32
	UFlags            uint32
	UCallbackMessage  uint32
	HIcon             uintptr
	SzTip             [128]uint16
	DwState           uint32
	DwStateMask       uint32
	SzInfo            [256]uint16
	UVersionOrTimeout uint32
	SzInfoTitle       [64]uint16
	DwInfoFlags       uint32
	GuidItem          [16]byte
	HBalloonIcon      uintptr
}

func (a *App) heartbeatWatchdog() {
	t := time.NewTicker(4 * time.Second)
	defer t.Stop()
	for range t.C {
		if atomic.LoadUint32(&a.heartbeatSeen) == 0 {
			continue
		}
		last := atomic.LoadInt64(&a.lastHeartbeat)
		if last > 0 && time.Now().UnixMilli()-last > 18000 {
			a.emit(Event{Type: "status", Status: "closing", Text: "تم إغلاق صفحة الاستديو، سيتم إغلاق البرنامج"})
			a.shutdownSoon("studio heartbeat ended")
			return
		}
	}
}

func (a *App) shutdownSoon(reason string) {
	go func() {
		time.Sleep(180 * time.Millisecond)
		a.stopLive()
		if a.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = a.srv.Shutdown(ctx)
			cancel()
		}
		os.Exit(0)
	}()
}

func (a *App) runTray() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() { _ = recover() }()
	className := utf16Ptr("XDreemB52_Device_Control_Tray_v096")
	hInst, _, _ := procGetModule.Call(0)
	hIcon := loadTrayIcon()
	var nid notifyIconData
	wndProc := syscall.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmTray:
			switch uint32(lParam) {
			case wmLButtonUp, wmLButtonDbl:
				openOrFocusDashboard()
				return 0
			case wmRButtonUp:
				a.showTrayMenu(hwnd)
				return 0
			}
		case wmCommand:
			switch uint32(wParam & 0xffff) {
			case cmdOpenDash:
				openOrFocusDashboard()
			case cmdExitApp:
				a.shutdownSoon("tray exit")
			}
			return 0
		case wmDestroy:
			procShellNotify.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
			procPostQuit.Call(0)
			return 0
		}
		r, _, _ := procDefWindow.Call(hwnd, uintptr(msg), wParam, lParam)
		return r
	})
	wc := wndClassEx{CbSize: uint32(unsafe.Sizeof(wndClassEx{})), LpfnWndProc: wndProc, HInstance: hInst, HIcon: hIcon, LpszClassName: className, HIconSm: hIcon}
	procRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, _ := procCreateWindow.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16Ptr(appName))), 0, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		return
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTray
	nid.HIcon = hIcon
	copyUTF16(nid.SzTip[:], appName+" · XDreemB52")
	added := false
	for _, sz := range []uint32{uint32(unsafe.Sizeof(nid)), 952, 936, 504} {
		nid.CbSize = sz
		if ok, _, _ := procShellNotify.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); ok != 0 {
			added = true
			break
		}
	}
	if !added {
		// Retry with the Windows application icon if the custom icon cannot be loaded.
		nid.HIcon, _, _ = procLoadIcon.Call(0, idiApplication)
		nid.CbSize = uint32(unsafe.Sizeof(nid))
		if ok, _, _ := procShellNotify.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); ok != 0 {
			added = true
		}
	}
	if added {
		nid.UVersionOrTimeout = notifyVersion4
		procShellNotify.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
		a.emit(Event{Type: "status", Status: "tray", Text: "ظهرت أيقونة التحكم في الجهاز بجانب الساعة"})
	} else {
		logToFile("tray add failed")
		a.emit(Event{Type: "status", Status: "warning", Text: "تعذر إظهار أيقونة الشريط بجانب الساعة"})
	}
	defer procShellNotify.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	var m winMsg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMsg.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (a *App) showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreateMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	procAppendMenu.Call(menu, mfString, cmdOpenDash, uintptr(unsafe.Pointer(utf16Ptr("فتح لوحة التحكم"))))
	procAppendMenu.Call(menu, mfString, cmdExitApp, uintptr(unsafe.Pointer(utf16Ptr("إغلاق التحكم في الجهاز"))))
	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	procSetForeground.Call(hwnd)
	cmd, _, _ := procTrackMenu.Call(menu, tpmRightButton|tpmRetCmd, uintptr(p.X), uintptr(p.Y), 0, hwnd, 0)
	switch uint32(cmd) {
	case cmdOpenDash:
		openOrFocusDashboard()
	case cmdExitApp:
		a.shutdownSoon("tray exit")
	}
}

func loadTrayIcon() uintptr {
	if len(trayIconBytes) > 0 {
		p := filepath.Join(dataDir(), "device-control-icon-v095.ico")
		_ = os.WriteFile(p, trayIconBytes, 0644)
		if h, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(utf16Ptr(p))), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize); h != 0 {
			return h
		}
	}
	h, _, _ := procLoadIcon.Call(0, idiApplication)
	return h
}

func (a *App) ensureDesktopShortcut() {
	time.Sleep(450 * time.Millisecond)
	exe, err := os.Executable()
	if err != nil || !strings.HasSuffix(strings.ToLower(exe), ".exe") {
		return
	}
	desktop := findDesktopDir()
	if desktop == "" {
		logToFile("desktop shortcut skipped: Desktop folder not found")
		return
	}
	shortcut := filepath.Join(desktop, appName+".lnk")
	target := strings.ReplaceAll(exe, "'", "''")
	working := strings.ReplaceAll(filepath.Dir(exe), "'", "''")
	link := strings.ReplaceAll(shortcut, "'", "''")
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $ws=New-Object -ComObject WScript.Shell; $s=$ws.CreateShortcut('%s'); $s.TargetPath='%s'; $s.WorkingDirectory='%s'; $s.IconLocation='%s,0'; $s.Description='%s'; $s.Save()`, link, target, working, target, appName)
	if err := startPS(script); err != nil {
		logToFile("desktop shortcut failed: " + err.Error())
		return
	}
	a.emit(Event{Type: "status", Status: "desktop-shortcut", Text: "تم تحديث اختصار التحكم في الجهاز على سطح المكتب"})
}

func findDesktopDir() string {
	candidates := []string{}
	if oneDrive := strings.TrimSpace(os.Getenv("OneDrive")); oneDrive != "" {
		candidates = append(candidates, filepath.Join(oneDrive, "Desktop"))
	}
	if profile := strings.TrimSpace(os.Getenv("USERPROFILE")); profile != "" {
		candidates = append(candidates, filepath.Join(profile, "Desktop"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "Desktop"))
	}
	for _, dir := range candidates {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return ""
}

func copyUTF16(dst []uint16, s string) {
	u := utf16.Encode([]rune(s))
	if len(u) >= len(dst) {
		u = u[:len(dst)-1]
	}
	copy(dst, u)
}

func utf16Ptr(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func openURL(u string) {
	procShell.Call(0, uintptr(unsafe.Pointer(utf16Ptr("open"))), uintptr(unsafe.Pointer(utf16Ptr(u))), 0, 0, showNormal)
}

func openOrFocusDashboard() {
	if focusDashboardWindow() {
		return
	}
	openURL(dashboard)
}

func focusDashboardWindow() bool {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		vis, _, _ := procIsWindowVis.Call(hwnd)
		if vis == 0 {
			return 1
		}
		ln, _, _ := procGetTextLen.Call(hwnd)
		if ln == 0 || ln > 512 {
			return 1
		}
		buf := make([]uint16, int(ln)+2)
		procGetText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		title := strings.ToLower(syscall.UTF16ToString(buf))
		if strings.Contains(title, "التحكم في الجهاز") || strings.Contains(title, "device-control") || strings.Contains(title, "studio multi-effects") {
			found = hwnd
			return 0
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	if found == 0 {
		return false
	}
	procShowWindow.Call(found, swRestore)
	procSetForeground.Call(found)
	return true
}

func existingAgentVersion() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 650*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/api/health", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer res.Body.Close()
	var v struct {
		Version string `json:"version"`
		OK      bool   `json:"ok"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&v) != nil || !v.OK {
		return "", false
	}
	return v.Version, true
}

func postExistingAgent(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+addr+path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err == nil && res != nil {
		res.Body.Close()
	}
}

func waitExistingAgentDown(max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if _, ok := existingAgentVersion(); !ok {
			return
		}
		time.Sleep(180 * time.Millisecond)
	}
}

func logToFile(s string) {
	_ = os.MkdirAll(dataDir(), 0755)
	f, err := os.OpenFile(filepath.Join(dataDir(), "device-control.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), s)
}

func (a *App) openDashboardSoon() { time.Sleep(900 * time.Millisecond); openOrFocusDashboard() }
