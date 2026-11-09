package server

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/athlon18/dafeiyu/stage"
	"github.com/athlon18/dafeiyu/web"
)

// assetExts 只放该被浏览器源取走的文件类型。白名单而不是黑名单：
// 目录里混着 .db/.log 之类的东西时，默认拒绝才不用记全所有敏感扩展名。
var assetExts = map[string]bool{
	".json": true, ".moc3": true, ".png": true, ".webp": true,
	".js": true, ".css": true, ".physics3.json": true,
}

// WithStage 接上现场状态：叠层页与后台预览都靠 /state 取"在不在说话、嘴张多大"。
func (s *Server) WithStage(st *stage.Stage) *Server { s.stage = st; return s }

// WithAssets 指定皮与渲染库的根目录（一般是 data/host）。
// 皮不入库（30MB 且作者禁止搬运），所以只能从磁盘给，不能内嵌。
func (s *Server) WithAssets(dir string) *Server { s.assets = dir; return s }

func (s *Server) registerStatic(mux *http.ServeMux) {
	mux.HandleFunc("/state", s.state)
	mux.HandleFunc("/overlay", s.embedded("overlay/overlay.html", "text/html; charset=utf-8"))
	mux.HandleFunc("/overlay.css", s.embedded("overlay/overlay.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/overlay.js", s.embedded("overlay/overlay.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("/admin", s.embedded("admin/admin.html", "text/html; charset=utf-8"))
	mux.HandleFunc("/admin.css", s.embedded("admin/admin.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/admin.js", s.embedded("admin/admin.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("/model/", s.diskAsset("model"))
	mux.HandleFunc("/lib/", s.diskAsset("lib"))
}

func (s *Server) embedded(name, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cors(w, r) {
			return
		}
		b, err := fs.ReadFile(web.FS, name)
		if err != nil {
			http.Error(w, "页面取不到: "+err.Error(), http.StatusNotFound)
			return
		}
		// 页面每次都要新的：主播改完配置刷新时不该拿到缓存的旧脚本（那会让人以为改动没生效）。
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write(b)
	}
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		return
	}
	if s.stage == nil {
		jsonOut(w, http.StatusOK, stage.State{})
		return
	}
	jsonOut(w, http.StatusOK, s.stage.Snapshot())
}

// diskAsset 从 data/host 下取文件。锁目录靠"逐段校验 + 白名单扩展名"，
// 不是靠 strings.HasPrefix——那种写法在 %2e%2e 编码穿越面前会漏。
func (s *Server) diskAsset(sub string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.assets == "" {
			http.Error(w, "没配素材目录", http.StatusNotFound)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/"+sub+"/")
		safe, ok := safeRel(rel)
		if !ok {
			http.Error(w, "路径不合法", http.StatusBadRequest)
			return
		}
		if !assetExts[strings.ToLower(filepath.Ext(safe))] {
			http.Error(w, "这类文件不提供", http.StatusNotFound)
			return
		}
		full := filepath.Join(s.assets, sub, filepath.FromSlash(safe))
		f, err := os.Open(full)
		if err != nil {
			http.Error(w, "取不到", http.StatusNotFound)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || stat.IsDir() {
			http.Error(w, "取不到", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", mimeOf(safe))
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeContent(w, r, safe, stat.ModTime(), f)
	}
}

// safeRel 只放行"素材目录下的相对路径"。
// 刻意不用 path.Clean 兜底：它会把 .. 吃掉后返回一个"看起来安全"的路径，
// 于是 /model/../lib/x 照样能跳到同级别的目录去——那是清洗后安全，不是拒绝。
func safeRel(rel string) (string, bool) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." {
			return "", false
		}
	}
	return rel, true
}

func mimeOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
