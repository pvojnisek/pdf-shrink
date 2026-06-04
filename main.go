package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	outDir     = "outputs"
	maxUpload  = 600 << 20 // 600 MB
	fileTTL    = 2 * time.Hour
	sweepEvery = 10 * time.Minute
	listenAddr = ":8080"
)

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	go sweeper()

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/shrink", handleShrink)
	http.HandleFunc("/dl/", handleDownload)

	addr := listenAddr
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("pdf-shrink listening on %s — outputs expire after %s", addr, fileTTL)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// availFile describes one still-downloadable result.
type availFile struct {
	ID        string
	Name      string
	Size      string
	MinsLeft  int
}

// listAvailable returns the not-yet-expired outputs, newest first.
func listAvailable() []availFile {
	entries, _ := os.ReadDir(outDir)
	now := time.Now()
	var out []availFile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".pdf") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		left := fileTTL - now.Sub(info.ModTime())
		if left <= 0 {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".pdf")
		out = append(out, availFile{
			ID:       id,
			Name:     displayName(id),
			Size:     humanMB(info.Size()),
			MinsLeft: int(left.Minutes()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTmpl.Execute(w, map[string]any{
		"Files":   listAvailable(),
		"TTLmins": int(fileTTL.Minutes()),
	})
}

func handleShrink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, "upload too large or malformed: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, hdr, err := r.FormFile("pdf")
	if err != nil {
		http.Error(w, "no file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	in, err := os.CreateTemp("", "upload-*.pdf")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	inPath := in.Name()
	defer os.Remove(inPath)
	inSize, err := io.Copy(in, file)
	in.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id := randID()
	base := strings.TrimSuffix(filepath.Base(hdr.Filename), ".pdf")
	if base == "" {
		base = "document"
	}
	outName := base + "-shrunk.pdf"
	outPath := filepath.Join(outDir, id+".pdf")

	st, err := Shrink(inPath, outPath)
	if err != nil {
		http.Error(w, "processing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Store the (possibly unicode) display name alongside the result.
	os.WriteFile(filepath.Join(outDir, id+".name"), []byte(outName), 0o644)

	fi, _ := os.Stat(outPath)
	st.InSize = inSize
	if fi != nil {
		st.OutSize = fi.Size()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	resultTmpl.Execute(w, map[string]any{
		"Link":    "/dl/" + id,
		"Name":    outName,
		"In":      humanMB(st.InSize),
		"Out":     humanMB(st.OutSize),
		"Saved":   savedPct(st.InSize, st.OutSize),
		"Keys":    st.StrippedKeys,
		"Images":  st.ImagesShrunk,
		"TTLmins": int(fileTTL.Minutes()),
	})
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/dl/")
	if id == "" || strings.ContainsAny(id, "/\\.") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(outDir, id+".pdf")
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "a fájl lejárt vagy nem található", http.StatusNotFound)
		return
	}
	name := displayName(id)
	// RFC 5987: ascii fallback + utf-8 encoded real name.
	ascii := toASCII(name)
	enc := strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", ascii, enc))
	w.Header().Set("Content-Type", "application/pdf")
	http.ServeFile(w, r, p)
}

// displayName reads the stored name for an id, falling back to <id>.pdf.
func displayName(id string) string {
	if b, err := os.ReadFile(filepath.Join(outDir, id+".name")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return id + ".pdf"
}

func sweeper() {
	for {
		entries, _ := os.ReadDir(outDir)
		now := time.Now()
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) > fileTTL {
				os.Remove(filepath.Join(outDir, e.Name()))
			}
		}
		time.Sleep(sweepEvery)
	}
}

func randID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func humanMB(n int64) string { return fmt.Sprintf("%.2f MB", float64(n)/1e6) }

func savedPct(in, out int64) string {
	if in == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(in-out)/float64(in))
}

// toASCII gives a plain-ASCII fallback filename for the Content-Disposition header.
func toASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "document.pdf"
	}
	return b.String()
}

var indexTmpl = template.Must(template.New("i").Funcs(template.FuncMap{
	"div": func(a, b int) int { return a / b },
}).Parse(`<!doctype html>
<html lang="hu"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PDF zsugorító</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:640px;margin:3rem auto;padding:0 1rem;color:#222}
 h1{font-size:1.4rem} h2{font-size:1.05rem;margin:2rem 0 .5rem}
 .card{border:1px solid #ddd;border-radius:12px;padding:1.5rem}
 input[type=file]{margin:1rem 0;display:block}
 button{font:inherit;padding:.6rem 1.2rem;border:0;border-radius:8px;background:#2563eb;color:#fff;cursor:pointer}
 button:disabled{background:#9ca3af} .muted{color:#666;font-size:.9rem}
 #spin{display:none;margin-top:1rem}
 ul.files{list-style:none;padding:0;margin:0}
 ul.files li{display:flex;justify-content:space-between;align-items:center;gap:1rem;
   padding:.6rem .8rem;border:1px solid #eee;border-radius:8px;margin-bottom:.5rem}
 ul.files a{color:#2563eb;text-decoration:none;font-weight:600;word-break:break-all}
 ul.files .meta{color:#666;font-size:.85rem;white-space:nowrap}
</style></head><body>
<h1>PDF zsugorító</h1>
<p class="muted">Töltsd fel a PDF-et. Eltávolítja az Apple Markup/PencilKit metaadatot, lekicsinyíti a nagy képeket, és újratömöríti.</p>
<div class="card">
 <form method="post" action="/shrink" enctype="multipart/form-data" onsubmit="document.getElementById('spin').style.display='block';this.querySelector('button').disabled=true">
  <input type="file" name="pdf" accept="application/pdf,.pdf" required>
  <button type="submit">Zsugorítás</button>
  <div id="spin">⏳ Feldolgozás… nagy fájlnál eltarthat egy kicsit.</div>
 </form>
</div>

<h2>Elérhető fájlok</h2>
{{if .Files}}
<ul class="files">
 {{range .Files}}
 <li>
   <a href="/dl/{{.ID}}">⬇ {{.Name}}</a>
   <span class="meta">{{.Size}} · még ~{{.MinsLeft}} perc</span>
 </li>
 {{end}}
</ul>
<p class="muted">A feldolgozott fájlok kb. {{.TTLmins}} percig ({{div .TTLmins 60}} óra) maradnak itt, utána automatikusan törlődnek.</p>
{{else}}
<p class="muted">Még nincs feldolgozott fájl. A zsugorítás után itt jelennek meg, és kb. {{.TTLmins}} percig tölthetők le.</p>
{{end}}
</body></html>`))

var resultTmpl = template.Must(template.New("r").Parse(`<!doctype html>
<html lang="hu"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Kész</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:640px;margin:3rem auto;padding:0 1rem;color:#222}
 .card{border:1px solid #ddd;border-radius:12px;padding:1.5rem}
 table{border-collapse:collapse;margin:1rem 0} td{padding:.25rem .75rem}
 a.btn{display:inline-block;padding:.6rem 1.2rem;border-radius:8px;background:#16a34a;color:#fff;text-decoration:none}
 .muted{color:#666;font-size:.9rem}
</style></head><body>
<div class="card">
 <h1>✅ Kész</h1>
 <table>
  <tr><td>Eredeti</td><td><b>{{.In}}</b></td></tr>
  <tr><td>Új méret</td><td><b>{{.Out}}</b></td></tr>
  <tr><td>Megtakarítás</td><td><b>{{.Saved}}</b></td></tr>
  <tr><td>Törölt Apple-kulcsok</td><td>{{.Keys}}</td></tr>
  <tr><td>Kicsinyített képek</td><td>{{.Images}}</td></tr>
 </table>
 <p><a class="btn" href="{{.Link}}">⬇ {{.Name}} letöltése</a></p>
 <p class="muted">A fájl kb. {{.TTLmins}} percig tölthető le, utána automatikusan törlődik.<br>
 <a href="/">← Kezdőlap / új fájl</a> — a korábbi fájlok is ott érhetők el.</p>
</div>
</body></html>`))
