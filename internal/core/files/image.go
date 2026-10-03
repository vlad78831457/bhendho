// Package files — файлы пользователей (ADR-16, ADR-60): приём картинок, хранилище, подписанные ссылки.
package files

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // декодер PNG
	"io"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // декодер WebP
)

var (
	// ErrTooLarge — файл больше предела.
	ErrTooLarge = errors.New("file is too large")
	// ErrUnsupported — не картинка JPEG, PNG или WebP (или повреждена).
	ErrUnsupported = errors.New("only JPEG, PNG and WebP images are accepted")
)

// maxPixels — предел размера картинки в пикселях до декодирования: маленький файл может
// объявить огромное полотно и съесть память при распаковке.
const maxPixels = 50_000_000

// Image — нормализованная картинка: всегда JPEG, не больше maxSide по большей стороне,
// повёрнута по EXIF камеры и без метаданных (геометка, модель телефона — ПДн).
type Image struct {
	Data          []byte
	Width, Height int
}

// Mime — тип нормализованной картинки.
const Mime = "image/jpeg"

// NormalizeImage читает не больше maxBytes и пересохраняет картинку.
func NormalizeImage(r io.Reader, maxBytes int64, maxSide int) (Image, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return Image{}, err
	}
	if int64(len(raw)) > maxBytes {
		return Image{}, ErrTooLarge
	}
	switch http.DetectContentType(raw) {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return Image{}, ErrUnsupported
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return Image{}, ErrUnsupported
	}
	if cfg.Width*cfg.Height > maxPixels {
		return Image{}, ErrTooLarge
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Image{}, ErrUnsupported
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxSide > 0 && (w > maxSide || h > maxSide) {
		if w >= h {
			w, h = maxSide, max(1, h*maxSide/w)
		} else {
			w, h = max(1, w*maxSide/h), maxSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Прозрачность PNG/WebP — на белом фоне: JPEG её не хранит.
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	// Поворот — после уменьшения: дешевле и без второй полноразмерной копии в памяти.
	oriented := orient(dst, jpegOrientation(raw))
	var out bytes.Buffer
	if err := jpeg.Encode(&out, oriented, &jpeg.Options{Quality: 88}); err != nil {
		return Image{}, fmt.Errorf("encode: %w", err)
	}
	return Image{Data: out.Bytes(), Width: oriented.Bounds().Dx(), Height: oriented.Bounds().Dy()}, nil
}
