package files

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// twoColors — картинка w×h: левая половина красная, правая синяя.
func twoColors(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// withExif — JPEG с сегментом EXIF: тег Orientation и «геометка» (текст, который не должен выжить).
func withExif(t *testing.T, img image.Image, orientation uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00")
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:], 3)
	binary.LittleEndian.PutUint32(entry[4:], 1)
	binary.LittleEndian.PutUint16(entry[8:], orientation)
	tiff = append(append(tiff, entry...), 0, 0, 0, 0)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	payload = append(payload, []byte("GPS 55.7558N 37.6173E")...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	raw := buf.Bytes()
	return append(append(append([]byte{}, raw[:2]...), seg...), raw[2:]...)
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xB000 && g < 0x5000 && b < 0x5000
}

func isBlue(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return b > 0xB000 && r < 0x5000 && g < 0x5000
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "jpeg" {
		t.Fatalf("result is not a JPEG: %v %s", err, format)
	}
	return img
}

func TestNormalizeRotatesByExifAndStripsMetadata(t *testing.T) {
	raw := withExif(t, twoColors(40, 20), 6) // 6 — повернуть на 90° по часовой
	if jpegOrientation(raw) != 6 {
		t.Fatal("orientation not read")
	}
	out, err := NormalizeImage(bytes.NewReader(raw), 1<<20, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 20 || out.Height != 40 {
		t.Fatalf("size after rotation: %dx%d", out.Width, out.Height)
	}
	img := decode(t, out.Data)
	// Левая (красная) половина после поворота по часовой — сверху.
	if !isRed(img.At(10, 5)) || !isBlue(img.At(10, 35)) {
		t.Fatalf("rotation is wrong: top %v, bottom %v", img.At(10, 5), img.At(10, 35))
	}
	if bytes.Contains(out.Data, []byte("Exif")) || bytes.Contains(out.Data, []byte("GPS")) {
		t.Fatal("metadata survived")
	}

	out, err = NormalizeImage(bytes.NewReader(withExif(t, twoColors(40, 20), 3)), 1<<20, 1024) // 180°
	if err != nil {
		t.Fatal(err)
	}
	img = decode(t, out.Data)
	if out.Width != 40 || !isBlue(img.At(5, 10)) || !isRed(img.At(35, 10)) {
		t.Fatal("180° rotation is wrong")
	}
}

func TestNormalizeScalesAndFlattens(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 300, 150)) // прозрачный PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	out, err := NormalizeImage(&buf, 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 100 || out.Height != 50 {
		t.Fatalf("scaled to %dx%d", out.Width, out.Height)
	}
	r, g, b, _ := decode(t, out.Data).At(50, 25).RGBA()
	if r < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Fatal("transparency must become white")
	}
}

func TestNormalizeRejects(t *testing.T) {
	if _, err := NormalizeImage(strings.NewReader("just text, not an image"), 1<<20, 1024); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("text: %v", err)
	}
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	if _, err := NormalizeImage(bytes.NewReader(gif), 1<<20, 1024); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("gif: %v", err)
	}
	raw := withExif(t, twoColors(40, 20), 1)
	if _, err := NormalizeImage(bytes.NewReader(raw), int64(len(raw)-1), 1024); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	broken := append([]byte{}, raw[:len(raw)/2]...)
	if _, err := NormalizeImage(bytes.NewReader(broken), 1<<20, 1024); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("truncated: %v", err)
	}
	// Заголовок PNG обещает полотно 100 000 × 100 000 — отказ до распаковки.
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1)))
	bomb := buf.Bytes()
	binary.BigEndian.PutUint32(bomb[16:], 100_000)
	binary.BigEndian.PutUint32(bomb[20:], 100_000)
	if _, err := NormalizeImage(bytes.NewReader(bomb), 1<<20, 1024); err == nil {
		t.Fatal("pixel bomb accepted")
	}
}

func TestSigner(t *testing.T) {
	s := NewSigner([]byte("secret-secret-secret-secret-secret"))
	now := time.Now()
	exp, sig := s.Sign("file-1", now.Add(time.Minute))
	e := func(n int64) string { return strconv.FormatInt(n, 10) }
	if !s.Valid("file-1", e(exp), sig, now) {
		t.Fatal("valid link rejected")
	}
	if s.Valid("file-2", e(exp), sig, now) || s.Valid("file-1", e(exp+60), sig, now) || s.Valid("file-1", e(exp), sig+"x", now) {
		t.Fatal("tampered link accepted")
	}
	if s.Valid("file-1", e(exp), sig, now.Add(2*time.Minute)) {
		t.Fatal("expired link accepted")
	}
	if NewSigner([]byte("other-secret-other-secret-other-!!")).Valid("file-1", e(exp), sig, now) {
		t.Fatal("link signed with another secret accepted")
	}
}

func TestLocalStore(t *testing.T) {
	ctx := context.Background()
	l := Local{Dir: t.TempDir()}
	key := "0f8fad5b-d9cb-469f-a165-70867728950e"
	if err := l.Put(ctx, key, []byte("photo")); err != nil {
		t.Fatal(err)
	}
	rc, err := l.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "photo" {
		t.Fatalf("read %q", got)
	}
	if err := l.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Open(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := l.Delete(ctx, key); err != nil {
		t.Fatal("second delete must be a no-op")
	}
	if err := l.Put(ctx, "../../etc/passwd", []byte("x")); err == nil {
		t.Fatal("path traversal key accepted")
	}
}
