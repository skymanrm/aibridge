package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	testPDF = []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
)

func TestFileKinds(t *testing.T) {
	cases := []struct {
		file File
		want string
	}{
		{File{Name: "a.png", Data: testPNG}, fileImage},
		{File{Name: "blob", Data: testPNG}, fileImage},
		{File{Name: "doc", Mime: "application/octet-stream", Data: testPDF}, filePDF},
		{File{Name: "notes.md", Data: []byte("# Notes")}, fileText},
		{File{Name: "data", Mime: "application/json; charset=utf-8", Data: []byte(`{"a":1}`)}, fileText},
		{File{Name: "a.bin", Data: []byte{0, 1, 2, 0xff}}, ""},
	}
	for _, c := range cases {
		if got := c.file.kind(); got != c.want {
			t.Errorf("%s: kind %q, want %q", c.file.Name, got, c.want)
		}
	}
	for name, want := range map[string]string{"../../etc/passwd": "passwd", `C:\x\My Report.pdf`: "My_Report.pdf", ".env": "env", "..": "file"} {
		if got := (File{Name: name}).safeName(); got != want {
			t.Errorf("safeName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFileValidation(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "x"}}
	for _, files := range [][]File{
		{{Name: "a.bin", Data: []byte{0, 1}}},
		{{Name: "empty.txt"}},
		make([]File, maxFiles+1),
		{{Name: "big.txt", Data: []byte(strings.Repeat("a", maxFilesBytes+1))}},
	} {
		if err := (ChatRequest{Messages: msgs, Files: files}).Validate(); err == nil {
			t.Errorf("accepted %d files", len(files))
		}
	}
	if err := (ChatRequest{Messages: msgs, Files: []File{{Name: "a.png", Data: testPNG}, {Name: "b.pdf", Data: testPDF}}}).Validate(); err != nil {
		t.Error(err)
	}
	if err := (ImageRequest{Prompt: "x", Files: []File{{Name: "b.pdf", Data: testPDF}}}).Validate(); err == nil {
		t.Error("image request accepted a PDF")
	}
}

func TestPromptInlinesTextFiles(t *testing.T) {
	req := ChatRequest{Messages: []Message{{Role: "user", Content: "Summarize"}},
		Files: []File{{Name: "notes.txt", Data: []byte("line\n")}, {Name: "a.png", Data: testPNG}}}
	if got := req.Prompt(); got != "<file name=\"notes.txt\">\nline\n</file>\n\nSummarize" {
		t.Errorf("prompt %q", got)
	}
}

func TestChatActivityOmitsFileData(t *testing.T) {
	fake := &fakeProvider{id: "fake", deltas: []string{"ok"}}
	s, cfg := newTestServer(t, fake)
	var last Activity
	s.OnActivity = func(a Activity) { last = a }
	data := base64.StdEncoding.EncodeToString(testPNG)
	rec := do(s, "POST", "/v1/chat", testOrigin, cfg.Token,
		`{"provider":"fake","messages":[{"role":"user","content":"hi"}],"files":[{"name":"a.png","data":"`+data+`"}]}`)
	if events := parseSSE(t, rec.Body.String()); events[len(events)-1].Name != "done" {
		t.Fatalf("events %v", events)
	}
	if len(fake.gotReq.Files) != 1 || string(fake.gotReq.Files[0].Data) != string(testPNG) {
		t.Errorf("provider files %+v", fake.gotReq.Files)
	}
	var req struct{ Files []fileSummary }
	if err := json.Unmarshal(last.Request, &req); err != nil || len(req.Files) != 1 ||
		req.Files[0] != (fileSummary{"a.png", "image/png", len(testPNG)}) || strings.Contains(string(last.Request), data) {
		t.Errorf("activity request %s (%v)", last.Request, err)
	}
}

func TestClaudeSendsAttachmentsAsContentBlocks(t *testing.T) {
	var args []string
	var stdin string
	p := &ClaudeProvider{Bin: "claude", Runner: fixtureRunner(
		[]string{`{"type":"result","subtype":"success","result":"ok"}`}, "", nil, &args, &stdin)}
	req := ChatRequest{Messages: []Message{{Role: "user", Content: "Compare"}},
		Files: []File{{Name: "a.png", Data: testPNG}, {Name: "b.pdf", Data: testPDF}, {Name: "c.txt", Data: []byte("hello")}}}
	if _, err := p.Run(context.Background(), req, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--input-format stream-json") {
		t.Errorf("args %v", args)
	}
	var msg struct {
		Type    string
		Message struct {
			Role    string
			Content []struct {
				Type   string
				Text   string
				Title  string
				Source struct {
					MediaType string `json:"media_type"`
					Data      []byte
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(stdin), &msg); err != nil {
		t.Fatalf("stdin %q: %v", stdin, err)
	}
	c := msg.Message.Content
	if msg.Type != "user" || len(c) != 3 || c[0].Type != "image" || c[0].Source.MediaType != "image/png" ||
		string(c[0].Source.Data) != string(testPNG) || c[1].Type != "document" || c[1].Title != "b.pdf" ||
		c[2].Type != "text" || !strings.Contains(c[2].Text, "hello") || !strings.HasSuffix(c[2].Text, "Compare") {
		t.Errorf("message %+v", msg)
	}
}

func TestCodexAttachesImagesAndPDFs(t *testing.T) {
	var args []string
	var stdin string
	var saved []string
	p := &CodexProvider{Bin: "codex", Runner: func(_ context.Context, _ string, a []string, dir, in string, _ []string, onLine func([]byte)) (string, error) {
		args, stdin = a, in
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, _ error) error {
			if !d.IsDir() {
				rel, _ := filepath.Rel(dir, path)
				saved = append(saved, filepath.ToSlash(rel))
			}
			return nil
		})
		onLine([]byte(`{"type":"turn.completed"}`))
		return "", nil
	}}
	req := ChatRequest{Messages: []Message{{Role: "user", Content: "Read"}},
		Files: []File{{Name: "a.png", Data: testPNG}, {Name: "b.pdf", Data: testPDF}}}
	if _, err := p.Run(context.Background(), req, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[0] != "files/1-a.png" || saved[1] != "files/2-b.pdf" {
		t.Errorf("saved %v", saved)
	}
	images := 0
	for _, a := range args {
		if strings.HasPrefix(a, "--image=") {
			images++
			if !strings.HasSuffix(filepath.ToSlash(a), "/files/1-a.png") {
				t.Errorf("image arg %q", a)
			}
		}
	}
	if images != 1 || args[len(args)-1] != "-" || !strings.Contains(stdin, "files/2-b.pdf") {
		t.Errorf("args %v stdin %q", args, stdin)
	}
}

func TestGeminiReferencesAttachments(t *testing.T) {
	var stdin string
	var pdf []byte
	p := &GeminiProvider{Bin: "gemini", Runner: func(_ context.Context, _ string, _ []string, dir, in string, _ []string, onLine func([]byte)) (string, error) {
		stdin = in
		pdf, _ = os.ReadFile(filepath.Join(dir, "files", "2-b.pdf"))
		onLine([]byte(`{"type":"message","role":"assistant","content":"ok"}`))
		onLine([]byte(`{"type":"result","status":"success"}`))
		return "", nil
	}}
	req := ChatRequest{Messages: []Message{{Role: "user", Content: "Read"}},
		Files: []File{{Name: "a.png", Data: testPNG}, {Name: "b.pdf", Data: testPDF}}}
	if _, err := p.Run(context.Background(), req, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stdin, "@files/1-a.png @files/2-b.pdf") || string(pdf) != string(testPDF) {
		t.Errorf("stdin %q pdf %q", stdin, pdf)
	}
}
