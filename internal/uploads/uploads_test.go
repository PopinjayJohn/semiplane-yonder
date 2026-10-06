package uploads

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateAcceptsKnownKinds(t *testing.T) {
	pngBytes := encodePNG(t, 8, 8)
	jpgBytes := encodeJPEG(t, 8, 8)
	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")
	webpBytes := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0}, 64)...)

	for _, tc := range []struct {
		name string
		data []byte
		file string
		mime string
	}{
		{"png", pngBytes, "shot.png", MIMEPNG},
		{"jpeg jpg", jpgBytes, "photo.jpg", MIMEJPEG},
		{"jpeg jpeg", jpgBytes, "photo.jpeg", MIMEJPEG},
		{"pdf", pdfBytes, "handout.pdf", MIMEPDF},
		{"webp stored", webpBytes, "pic.webp", MIMEWebP},
		{"no extension allowed", pngBytes, "blob", MIMEPNG},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mime, err := Validate(tc.data, tc.file)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if mime != tc.mime {
				t.Errorf("mime = %s, want %s", mime, tc.mime)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	pngBytes := encodePNG(t, 8, 8)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte(`<html><body>hi</body></html>`)
	big := append(encodePNG(t, 8, 8), bytes.Repeat([]byte{0}, MaxImageBytes)...)

	for _, tc := range []struct {
		name string
		data []byte
		file string
		want error
	}{
		{"svg blocked", svg, "pic.svg", ErrBlockedType},
		{"html blocked", html, "pic.html", ErrBlockedType},
		{"text unsupported", []byte("just some text notes here....."), "n.txt", ErrUnsupportedType},
		{"oversize", big, "big.png", ErrTooLarge},
		{"extension mismatch", pngBytes, "pic.jpg", ErrTypeMismatch},
		{"path traversal", pngBytes, "../evil.png", ErrBadFilename},
		{"backslash", pngBytes, `sub\evil.png`, ErrBadFilename},
		{"reserved con", pngBytes, "CON.png", ErrBadFilename},
		{"reserved nul", pngBytes, "nul", ErrBadFilename},
		{"reserved lpt1", pngBytes, "LPT1.md", ErrBadFilename},
		{"trailing dot", pngBytes, "name.", ErrBadFilename},
		{"empty name", pngBytes, "", ErrBadFilename},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Validate(tc.data, tc.file); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNormalizeResizesAndReencodes(t *testing.T) {
	big := encodePNG(t, 2000, 1000)
	out, mime, err := Process(big, "map.png", DisplayMaxDim)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if mime != MIMEPNG {
		t.Fatalf("mime = %s", mime)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output must decode: %v", err)
	}
	if cfg.Width != DisplayMaxDim || cfg.Height != 800 {
		t.Errorf("resized to %dx%d, want %dx800", cfg.Width, cfg.Height, DisplayMaxDim)
	}

	// Small images keep dimensions but still round-trip (EXIF strip path).
	small := encodeJPEG(t, 40, 30)
	out, _, err = Process(small, "face.jpg", DisplayMaxDim)
	if err != nil {
		t.Fatalf("Process small: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-encoded output must decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 30 {
		t.Errorf("small image became %dx%d, want 40x30", b.Dx(), b.Dy())
	}
}

func TestNormalizeStoresOpaqueKindsAsIs(t *testing.T) {
	pdf := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")
	out, err := Normalize(pdf, MIMEPDF, DisplayMaxDim)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, pdf) {
		t.Error("pdf must be stored byte-identical")
	}
}

func FuzzDetectMIME(f *testing.F) {
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte("%PDF-1.4"))
	f.Add([]byte("<svg "))
	f.Add([]byte("hello"))
	f.Fuzz(func(t *testing.T, data []byte) {
		mime, err := DetectMIME(data)
		if err == nil && MaxBytesFor(mime) < 0 {
			t.Fatalf("accepted MIME %q has no size cap", mime)
		}
	})
}

func FuzzValidateName(f *testing.F) {
	f.Add("map.png")
	f.Add("CON")
	f.Add("../x")
	f.Fuzz(func(t *testing.T, name string) {
		_ = ValidateName(name) // must never panic
		if strings.ContainsAny(name, "/\\") && ValidateName(name) == nil {
			t.Fatalf("separator in %q accepted", name)
		}
	})
}
