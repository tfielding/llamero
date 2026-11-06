package bot

// Photos sent to the bot: downloaded, shrunk and converted to JPEG for a vision model.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/tfielding/signal-ollama/internal/signalcli"
)

const (
	maxPhotos    = 4    // photos per message given to the model
	maxPhotoSide = 1600 // longest side after shrinking; vision models scale down further anyway
)

// photoText stands in for the text of a message that's only a photo.
const photoText = "(The user sent this photo without a message.)"

// noVisionText explains that no model that can see pictures is available.
func noVisionText(model string) string {
	return fmt.Sprintf("%s can't see pictures. Switch to a model that can with /models, or set OLLAMA_VISION_MODEL "+
		"(e.g. a gemma or qwen-vl model) to use one just for photos.", model)
}

// loadPhotos downloads the photos among atts and returns them as JPEGs, plus a note for the model
// about anything it couldn't use (other files, too many photos, unreadable pictures).
func (b *Bot) loadPhotos(ctx context.Context, atts []signalcli.ReceivedAttachment) (photos [][]byte, note string) {
	var skipped []string
	for _, a := range atts {
		if !strings.HasPrefix(strings.ToLower(a.ContentType), "image/") {
			skipped = append(skipped, describeFile(a))
			continue
		}
		if len(photos) == maxPhotos {
			skipped = append(skipped, "a photo past the first "+fmt.Sprint(maxPhotos))
			continue
		}
		data, err := b.msg.Attachment(ctx, a.ID)
		if err != nil {
			slog.Warn("downloading photo failed", "type", a.ContentType, "err", err)
			skipped = append(skipped, "a photo that couldn't be downloaded")
			continue
		}
		jpg, err := toJPEG(data)
		if err != nil {
			slog.Warn("reading photo failed", "type", a.ContentType, "err", err)
			skipped = append(skipped, "a picture in a format that can't be read ("+a.ContentType+")")
			continue
		}
		photos = append(photos, jpg)
	}
	if len(skipped) > 0 {
		note = "(The user also sent " + strings.Join(skipped, ", ") + ", which you can't see.)"
	}
	return photos, note
}

func describeFile(a signalcli.ReceivedAttachment) string {
	switch t := strings.ToLower(a.ContentType); {
	case strings.HasPrefix(t, "audio/"):
		return "a voice note or audio file"
	case strings.HasPrefix(t, "video/"):
		return "a video"
	case a.Filename != "":
		return "a file (" + a.Filename + ")"
	}
	return "a file"
}

// toJPEG decodes a JPEG, PNG, GIF (first frame) or WebP picture, shrinks it so its longest side
// is at most maxPhotoSide, and encodes it as JPEG.
func toJPEG(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bnd := img.Bounds()
	if w, h := bnd.Dx(), bnd.Dy(); max(w, h) > maxPhotoSide {
		scale := float64(maxPhotoSide) / float64(max(w, h))
		dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, bnd, draw.Src, nil) // fast, and plenty for a model
		img = dst
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// stillImage reports whether a vision model can be given a picture of this type.
func stillImage(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg", "image/png":
		return true
	}
	return false
}
