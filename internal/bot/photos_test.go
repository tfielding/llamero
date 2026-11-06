package bot

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/tfielding/signal-ollama/internal/ollama"
	"github.com/tfielding/signal-ollama/internal/signalcli"
	"github.com/tfielding/signal-ollama/internal/toolbox"
)

// picture encodes a w×h picture of one colour as PNG or JPEG.
func picture(t *testing.T, w, h int, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{0, 160, 0, 255}), image.Point{}, draw.Src)
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// photoMsg is a message with attachments, as signal-cli-rest-api delivers it.
func photoMsg(text string, atts ...signalcli.ReceivedAttachment) signalcli.Envelope {
	env := dm(me, text)
	env.DataMessage.Attachments = atts
	return env
}

func jpg(id string) signalcli.ReceivedAttachment {
	return signalcli.ReceivedAttachment{ID: id, ContentType: "image/jpeg"}
}

func TestPhotoGoesToTheVisionModel(t *testing.T) {
	b, m, l, st := setup(t)
	m.files = map[string][]byte{"p1": picture(t, 2000, 1000, "png")}
	b.opt.VisionModel = "llama3.2:latest" // the fake reports capabilities from l.caps for any other model
	l.caps = []string{"vision"}
	b.opt.DefaultModel = "qwen2.5:7b" // can't see (no vision in its metadata)

	b.Handle(photoMsg("what plant is this?", signalcli.ReceivedAttachment{ID: "p1", ContentType: "image/png"}))
	reply(t, m)
	l.mu.Lock()
	model, last := l.model, l.last[len(l.last)-1]
	l.mu.Unlock()
	if model != "llama3.2:latest" {
		t.Errorf("answered by %s, want the vision model", model)
	}
	if last.Content != "what plant is this?" || len(last.Images) != 1 {
		t.Fatalf("message: %q with %d images", last.Content, len(last.Images))
	}
	// Shrunk to at most 1600 px and sent as JPEG.
	img, err := jpeg.Decode(bytes.NewReader(last.Images[0]))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	if bd := img.Bounds(); bd.Dx() != 1600 || bd.Dy() != 800 {
		t.Errorf("size %v, want 1600x800", bd)
	}
	// The photo isn't kept in the history, just a note; the chat keeps its own model.
	chat, _ := st.Load(me)
	if len(chat.History) != 2 || chat.History[0].Content != "what plant is this?\n\n[The user sent 1 photo(s) with this message.]" ||
		len(chat.History[0].Images) != 0 || chat.Model != "" {
		t.Errorf("history: %+v, model %q", chat.History, chat.Model)
	}
	if chat.Last == nil || chat.Last.Model != "llama3.2:latest" {
		t.Errorf("last reply's model: %+v", chat.Last)
	}
}

func TestPhotoWithoutText(t *testing.T) {
	b, m, l, _ := setup(t)
	m.files = map[string][]byte{"p1": picture(t, 200, 100, "jpeg")}
	l.caps = []string{"vision"} // the chat's model can see: no switch
	b.Handle(photoMsg("", jpg("p1")))
	if got := reply(t, m); !strings.Contains(got, photoText) {
		t.Fatalf("got %q", got)
	}
	l.mu.Lock()
	model := l.model
	l.mu.Unlock()
	if model != "llama3.2:latest" {
		t.Errorf("model %s", model)
	}
}

func TestNoModelCanSee(t *testing.T) {
	b, m, l, _ := setup(t)
	m.files = map[string][]byte{"p1": picture(t, 200, 100, "jpeg")}
	b.Handle(photoMsg("what's this?", jpg("p1")))
	if got := reply(t, m); !strings.Contains(got, "llama3.2:latest can't see pictures") || !strings.Contains(got, "OLLAMA_VISION_MODEL") {
		t.Fatalf("got %q", got)
	}
	// A vision model that can't actually see is reported as such.
	b.opt.VisionModel = "qwen2.5:7b"
	b.Handle(photoMsg("what's this?", jpg("p1")))
	if got := reply(t, m); !strings.Contains(got, "OLLAMA_VISION_MODEL qwen2.5:7b can't see pictures") {
		t.Fatalf("got %q", got)
	}
	l.mu.Lock()
	calls := l.calls
	l.mu.Unlock()
	if calls != 0 {
		t.Errorf("the model was asked %d times without being able to see", calls)
	}
}

func TestFilesAndBadPhotos(t *testing.T) {
	b, m, l, _ := setup(t)
	l.caps = []string{"vision"}
	m.files = map[string][]byte{"p1": picture(t, 200, 100, "jpeg"), "bad": []byte("not a picture")}

	// Only a voice note: told plainly, no model call.
	b.Handle(photoMsg("", signalcli.ReceivedAttachment{ID: "v1", ContentType: "audio/aac"}))
	if got := reply(t, m); got != "I can only look at photos for now, not files, videos or voice notes." {
		t.Fatalf("voice note: %q", got)
	}
	// A photo that can't be read, alone.
	b.Handle(photoMsg("", jpg("bad")))
	if got := reply(t, m); !strings.Contains(got, "couldn't read that photo") {
		t.Fatalf("bad photo: %q", got)
	}
	// A photo plus a PDF: the photo is seen, the model is told about the file it can't open.
	b.Handle(photoMsg("compare these", jpg("p1"), signalcli.ReceivedAttachment{ID: "d1", ContentType: "application/pdf", Filename: "invoice.pdf"}))
	reply(t, m)
	l.mu.Lock()
	last := l.last[len(l.last)-1]
	l.mu.Unlock()
	if len(last.Images) != 1 || !strings.Contains(last.Content, "(The user also sent a file (invoice.pdf), which you can't see.)") {
		t.Fatalf("message: %q, %d images", last.Content, len(last.Images))
	}
	l.mu.Lock()
	l.calls = 0
	l.mu.Unlock()
}

func TestTooManyPhotos(t *testing.T) {
	b, m, l, _ := setup(t)
	l.caps = []string{"vision"}
	m.files = map[string][]byte{}
	var atts []signalcli.ReceivedAttachment
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		m.files[id] = picture(t, 50, 50, "jpeg")
		atts = append(atts, jpg(id))
	}
	b.Handle(photoMsg("which is best?", atts...))
	reply(t, m)
	l.mu.Lock()
	last := l.last[len(l.last)-1]
	l.mu.Unlock()
	if len(last.Images) != maxPhotos || !strings.Contains(last.Content, "a photo past the first 4") {
		t.Fatalf("%d images, %q", len(last.Images), last.Content)
	}
}

func TestToJPEG(t *testing.T) {
	small := picture(t, 640, 480, "png")
	out, err := toJPEG(small)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 480 {
		t.Fatalf("small picture changed size: %v %v", img.Bounds(), err)
	}
	tall, _ := toJPEG(picture(t, 1000, 4000, "jpeg"))
	img, _ = jpeg.Decode(bytes.NewReader(tall))
	if img.Bounds().Dx() != 400 || img.Bounds().Dy() != 1600 {
		t.Fatalf("portrait: %v", img.Bounds())
	}
	if _, err := toJPEG([]byte("%PDF-1.7")); err == nil {
		t.Error("a PDF was read as a picture")
	}
}

func TestToolClipNotGivenToTheModel(t *testing.T) {
	b, m, l, _ := setup(t)
	l.caps = []string{"tools", "vision"}
	tc := &ollama.ToolCall{}
	tc.Function.Name = "get_alert_clip"
	l.toolCall = tc
	b.opt.Tools = &fakeTools{
		tools: []toolbox.Tool{{Name: "get_alert_clip", Schema: lightSchema, ReadOnly: true}},
		out:   []toolbox.Image{{MIME: "image/jpeg", Data: []byte("jpg")}, {MIME: "image/gif", Data: []byte("GIF89a")}},
	}
	b.Handle(dm(me, "show me what happened"))
	reply(t, m)

	l.mu.Lock()
	var toolMsg ollama.Message
	for _, msg := range l.last {
		if msg.Role == "tool" {
			toolMsg = msg
		}
	}
	l.mu.Unlock()
	// The model sees the still picture, not the clip, and knows about both.
	if len(toolMsg.Images) != 1 || string(toolMsg.Images[0]) != "jpg" ||
		!strings.Contains(toolMsg.Content, "[1 image(s) attached.") ||
		!strings.Contains(toolMsg.Content, "[1 image(s) or clip(s) returned. They will be sent to the user with your reply; you cannot see them.]") {
		t.Fatalf("tool message: %q with %d images", toolMsg.Content, len(toolMsg.Images))
	}
	// The user gets both, the clip as a .gif.
	m.mu.Lock()
	attached := strings.Join(m.attached, ", ")
	m.mu.Unlock()
	if attached != "image/jpeg image-1.jpg, image/gif image-2.gif" {
		t.Errorf("attachments sent: %s", attached)
	}
}

func TestStillImage(t *testing.T) {
	for mime, want := range map[string]bool{"image/jpeg": true, "IMAGE/PNG": true, "image/gif": false, "image/webp": false, "": false} {
		if got := stillImage(mime); got != want {
			t.Errorf("stillImage(%q) = %v", mime, got)
		}
	}
}
