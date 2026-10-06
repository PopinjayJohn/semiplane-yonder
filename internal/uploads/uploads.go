// Package uploads implements the P13 upload pipeline validation and
// normalization: MIME sniffing (never file extension), size caps,
// Windows-safe filenames, the SVG blocklist, and stdlib-only image
// resize/EXIF-strip.
//
// Only the frozen dependency set is used (spec §2): stdlib image/png,
// image/jpeg plus golang.org/x/image/draw (CatmullRom) for scaling.
// webp and PDF have no decoder in the frozen set, so they are stored as-is
// after sniff + size cap (P13). EXIF is stripped by decode+re-encode: Go's
// decoders ignore EXIF segments, so re-encoded bytes carry no EXIF —
// orientation tags included, meaning rotated phone photos need re-saving
// before upload (P13).
//
// Wiring into HTTP handlers and asset storage belongs to the web lanes;
// this package is pure and handler-free.
package uploads

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"path"
	"strings"

	"golang.org/x/image/draw"
)

const (
	// MaxImageBytes caps raster images and webp (P13: 5MB image).
	MaxImageBytes = 5 * 1024 * 1024
	// MaxPDFBytes caps PDFs (P13: 10MB PDF, size-capped only).
	MaxPDFBytes = 10 * 1024 * 1024
	// DisplayMaxDim is the longest stored edge for resized raster images.
	DisplayMaxDim = 1600
	// MaxPixels bounds decoding to avoid OOM on hostile dimensions.
	MaxPixels = 32 * 1024 * 1024
)

var (
	ErrTooLarge        = errors.New("uploads: file exceeds size cap")
	ErrUnsupportedType = errors.New("uploads: unsupported media type")
	ErrBlockedType     = errors.New("uploads: blocked media type")
	ErrBadFilename     = errors.New("uploads: unsafe filename")
	ErrTooManyPixels   = errors.New("uploads: image dimensions exceed processing limit")
	ErrTypeMismatch    = errors.New("uploads: extension does not match sniffed content")
)

// MIME kinds accepted by DetectMIME.
const (
	MIMEPNG  = "image/png"
	MIMEJPEG = "image/jpeg"
	MIMEWebP = "image/webp"
	MIMEPDF  = "application/pdf"
)

// MaxBytesFor returns the size cap for a sniffed MIME type, or -1 when the
// type is not an accepted upload kind.
func MaxBytesFor(mime string) int {
	switch mime {
	case MIMEPNG, MIMEJPEG, MIMEWebP:
		return MaxImageBytes
	case MIMEPDF:
		return MaxPDFBytes
	default:
		return -1
	}
}

// DetectMIME sniffs the content type from the leading bytes, never from the
// filename. SVG/HTML/XML are blocked outright (scriptable content); anything
// outside png/jpeg/webp/pdf is rejected as unsupported.
func DetectMIME(data []byte) (string, error) {
	// net/http does not class bare "<svg ..." as HTML/XML, so the SVG
	// blocklist needs its own prefix check; rejection must not depend on
	// which text flavor the sniffer happens to report.
	lead := bytes.ToLower(bytes.TrimSpace(firstBytes(data, 512)))
	if bytes.HasPrefix(lead, []byte("<svg")) {
		return "", fmt.Errorf("%w: svg", ErrBlockedType)
	}
	mime, _, _ := strings.Cut(http.DetectContentType(data), ";")
	switch mime {
	case MIMEPNG, MIMEJPEG, MIMEPDF, MIMEWebP:
		return mime, nil
	case "image/svg+xml", "text/html", "text/xml", "application/xml":
		return "", fmt.Errorf("%w: %s", ErrBlockedType, mime)
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedType, mime)
	}
}

// windowsReserved lists case-insensitive stem names Windows refuses as files
// (with or without an extension).
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateName rejects names that are unsafe on Windows or usable for path
// traversal. It validates; sanitization (if any) is the caller's concern.
func ValidateName(name string) error {
	if name == "" || len(name) > 255 {
		return fmt.Errorf("%w: %q", ErrBadFilename, name)
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("%w: %q", ErrBadFilename, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: control character in %q", ErrBadFilename, name)
		}
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("%w: trailing space/dot in %q", ErrBadFilename, name)
	}
	stem := name
	if ext := path.Ext(name); ext != "" {
		stem = strings.TrimSuffix(name, ext)
	}
	if windowsReserved[strings.ToLower(stem)] {
		return fmt.Errorf("%w: reserved name %q", ErrBadFilename, name)
	}
	return nil
}

// extsForMIME lists the only filename extensions accepted per sniffed type.
// An empty extension is allowed (client omitted it); a wrong one fails
// closed because the client misdeclared the content.
func extsForMIME(mime string) []string {
	switch mime {
	case MIMEPNG:
		return []string{".png"}
	case MIMEJPEG:
		return []string{".jpg", ".jpeg"}
	case MIMEPDF:
		return []string{".pdf"}
	case MIMEWebP:
		return []string{".webp"}
	default:
		return nil
	}
}

// Validate sniffs, caps, and filename-checks an upload, returning the
// trusted MIME type. Limits are enforced on the full in-memory payload;
// handlers must additionally enforce body limits while streaming to temp
// (pitfalls: uploads).
func Validate(data []byte, filename string) (string, error) {
	if err := ValidateName(filename); err != nil {
		return "", err
	}
	mime, err := DetectMIME(data)
	if err != nil {
		return "", err
	}
	if cap := MaxBytesFor(mime); cap >= 0 && len(data) > cap {
		return "", fmt.Errorf("%w: %s is %d bytes (cap %d)", ErrTooLarge, mime, len(data), cap)
	}
	if ext := strings.ToLower(path.Ext(filename)); ext != "" {
		ok := false
		for _, want := range extsForMIME(mime) {
			if ext == want {
				ok = true
				break
			}
		}
		if !ok {
			return "", fmt.Errorf("%w: %q is %s", ErrTypeMismatch, filename, mime)
		}
	}
	return mime, nil
}

// Normalize post-processes validated payloads: raster images are
// decoded, downscaled to maxDim when larger, and re-encoded (which strips
// EXIF); webp and PDF are stored as-is. It returns the bytes to store.
func Normalize(data []byte, mime string, maxDim int) ([]byte, error) {
	switch mime {
	case MIMEWebP, MIMEPDF:
		return data, nil
	case MIMEPNG, MIMEJPEG:
		// Decode explicitly per sniffed type so a mismatched codec
		// fails here rather than producing a corrupt store.
		var (
			img image.Image
			err error
		)
		if mime == MIMEPNG {
			img, err = png.Decode(bytes.NewReader(data))
		} else {
			img, err = jpeg.Decode(bytes.NewReader(data))
		}
		if err != nil {
			return nil, fmt.Errorf("uploads: decode %s: %w", mime, err)
		}
		b := img.Bounds()
		w, h := int64(b.Dx()), int64(b.Dy())
		if w*h > MaxPixels {
			return nil, fmt.Errorf("%w: %dx%d", ErrTooManyPixels, w, h)
		}
		if max(int(w), int(h)) > maxDim {
			img = scaleDown(img, maxDim)
		}
		var buf bytes.Buffer
		if mime == MIMEPNG {
			err = png.Encode(&buf, img)
		} else {
			err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
		}
		if err != nil {
			return nil, fmt.Errorf("uploads: re-encode %s: %w", mime, err)
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, mime)
	}
}

// Process validates then normalizes an upload in one call, returning the
// bytes to store and the trusted MIME type.
func Process(data []byte, filename string, maxDim int) ([]byte, string, error) {
	mime, err := Validate(data, filename)
	if err != nil {
		return nil, "", err
	}
	out, err := Normalize(data, mime, maxDim)
	if err != nil {
		return nil, "", err
	}
	return out, mime, nil
}

// firstBytes returns up to n leading bytes for prefix sniffing.
func firstBytes(data []byte, n int) []byte {
	if len(data) > n {
		return data[:n]
	}
	return data
}

func scaleDown(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := max(w, h)
	nw, nh := w*maxDim/longest, h*maxDim/longest
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}
