package bridge

import (
	"bytes"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxFiles      = 20
	maxFilesBytes = 32 << 20
)

// File is an attachment; Data is base64-encoded in JSON.
type File struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data []byte `json:"data"`
}

// File kinds: images and PDFs are passed to the CLI natively, text is inlined into the prompt.
const (
	fileImage = "image"
	filePDF   = "pdf"
	fileText  = "text"
)

var imageFileMimes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

var kindExts = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
	"application/pdf": ".pdf"}

// mimeType is the declared type, else guessed from the extension, else sniffed from the content.
func (f File) mimeType() string {
	m := f.Mime
	if m == "" || m == "application/octet-stream" {
		m = mime.TypeByExtension(strings.ToLower(filepath.Ext(f.Name)))
	}
	if m == "" {
		m = http.DetectContentType(f.Data)
	}
	if base, _, err := mime.ParseMediaType(m); err == nil {
		return base
	}
	return strings.ToLower(m)
}

// kind returns fileImage, filePDF, fileText or "" for unsupported files.
func (f File) kind() string {
	m := f.mimeType()
	switch {
	case imageFileMimes[m]:
		return fileImage
	case m == "application/pdf" || bytes.HasPrefix(f.Data, []byte("%PDF-")):
		return filePDF
	case utf8.Valid(f.Data) && !bytes.ContainsRune(f.Data, 0):
		return fileText
	}
	return ""
}

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeName is the file's base name reduced to characters that are safe in paths and CLI prompts.
func (f File) safeName() string {
	name := filepath.Base(strings.ReplaceAll(f.Name, `\`, "/"))
	name = strings.TrimLeft(unsafeNameChars.ReplaceAllString(name, "_"), ".-_")
	if len(name) > 100 {
		name = name[len(name)-100:]
	}
	if name == "" || name == "." {
		name = "file" + kindExts[f.mimeType()]
	}
	return name
}

// validateFiles checks limits and types; allowed lists the accepted kinds.
func validateFiles(files []File, allowed ...string) error {
	if len(files) > maxFiles {
		return &ProviderError{"validation_error", fmt.Sprintf("at most %d files can be attached", maxFiles)}
	}
	total := 0
	for i, f := range files {
		if len(f.Data) == 0 {
			return &ProviderError{"validation_error", fmt.Sprintf("files[%d] (%s) is empty", i, f.Name)}
		}
		total += len(f.Data)
		if k := f.kind(); k == "" || !slices.Contains(allowed, k) {
			return &ProviderError{"validation_error", fmt.Sprintf("files[%d] (%s): unsupported type %s; accepted: %s",
				i, f.Name, f.mimeType(), strings.Join(allowed, ", "))}
		}
	}
	if total > maxFilesBytes {
		return &ProviderError{"validation_error", fmt.Sprintf("attached files exceed %d MB", maxFilesBytes>>20)}
	}
	return nil
}

// attachment is a binary file saved for a CLI run, at Path relative to the run directory.
type attachment struct {
	File
	Kind string
	Path string
}

// attachments lists the image and PDF files with unique paths under files/.
func attachments(files []File) []attachment {
	var out []attachment
	for _, f := range files {
		if k := f.kind(); k == fileImage || k == filePDF {
			out = append(out, attachment{f, k, fmt.Sprintf("files/%d-%s", len(out)+1, f.safeName())})
		}
	}
	return out
}

func writeAttachments(dir string, atts []attachment) error {
	for _, a := range atts {
		path := filepath.Join(dir, filepath.FromSlash(a.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, a.Data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// inlineTextFiles renders text attachments as <file> blocks placed before the prompt.
func inlineTextFiles(files []File) string {
	var b strings.Builder
	for _, f := range files {
		if f.kind() == fileText {
			fmt.Fprintf(&b, "<file name=%q>\n%s\n</file>\n\n", f.safeName(), strings.TrimRight(string(f.Data), "\n"))
		}
	}
	return b.String()
}

// fileSummary stands in for an attachment in activity records, without its contents.
type fileSummary struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int    `json:"size"`
}

func summarizeFiles(files []File) []fileSummary {
	out := make([]fileSummary, len(files))
	for i, f := range files {
		out[i] = fileSummary{f.Name, f.mimeType(), len(f.Data)}
	}
	return out
}
